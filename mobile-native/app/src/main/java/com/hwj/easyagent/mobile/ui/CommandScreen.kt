package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Card
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.unit.dp
import com.hwj.easyagent.mobile.backend.ApiClient
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive

private val commandJson = Json { ignoreUnknownKeys = true }

private data class CommandGoal(
    val number: Int,
    val title: String,
    val status: String,
    val done: List<Pair<String, Boolean>>,
)

private data class CommandInboxItem(
    val id: String,
    val kind: String,
    val title: String,
    val body: String,
)

private fun JsonElement?.str(field: String): String =
    this?.jsonObject?.get(field)?.let { runCatching { it.jsonPrimitive.content }.getOrNull() } ?: ""

private fun JsonElement?.intValue(field: String): Int =
    this?.jsonObject?.get(field)?.let { runCatching { it.jsonPrimitive.content.toInt() }.getOrNull() } ?: 0

private fun parseGoals(el: JsonElement?): List<CommandGoal> = runCatching {
    el?.jsonArray?.map { g ->
        CommandGoal(
            number = g.intValue("number"),
            title = g.str("title"),
            status = g.str("status"),
            done = runCatching {
                g.jsonObject["done"]?.jsonArray?.map { d ->
                    Pair(d.str("text"), d.str("check") == "true")
                } ?: emptyList()
            }.getOrDefault(emptyList()),
        )
    } ?: emptyList()
}.getOrDefault(emptyList())

private fun parseInbox(el: JsonElement?): List<CommandInboxItem> = runCatching {
    el?.jsonArray?.map { i ->
        CommandInboxItem(id = i.str("id"), kind = i.str("kind"), title = i.str("title"), body = i.str("body"))
    } ?: emptyList()
}.getOrDefault(emptyList())

/** 手机端指挥台：只做决定，不做实现——收件箱 + 目标，每件事一屏内处理完。 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun CommandScreen(client: ApiClient, onBack: () -> Unit) {
    val scope = rememberCoroutineScope()
    val clipboard = LocalClipboardManager.current
    var inbox by remember { mutableStateOf(emptyList<CommandInboxItem>()) }
    var goals by remember { mutableStateOf(emptyList<CommandGoal>()) }
    var refreshing by remember { mutableStateOf(false) }
    var openGoal by remember { mutableStateOf<Int?>(null) }
    var message by remember { mutableStateOf<String?>(null) }

    suspend fun refresh() {
        refreshing = true
        runCatching {
            val state = client.get("/command/state").jsonObject
            goals = parseGoals(state["goals"])
            inbox = parseInbox(state["inbox"])
        }
        refreshing = false
    }

    LaunchedEffect(Unit) {
        refresh()
        while (true) { // 30s 轮询，与网页端一致
            delay(30_000)
            refresh()
        }
    }

    Scaffold(topBar = {
        TopAppBar(
            title = { Text("指挥台") },
            navigationIcon = {
                IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "返回") }
            },
            actions = {
                IconButton(onClick = { scope.launch { refresh() } }) {
                    Icon(Icons.Filled.Refresh, contentDescription = "刷新")
                }
            },
        )
    }) { pad ->
        Column(modifier = Modifier.padding(pad).fillMaxSize()) {
            LazyColumn(
                modifier = Modifier.fillMaxSize().padding(horizontal = 12.dp),
                verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                item {
                    Text(
                        "收件箱",
                        style = MaterialTheme.typography.titleSmall,
                        modifier = Modifier.padding(top = 12.dp, bottom = 4.dp),
                    )
                }
                if (inbox.isEmpty()) {
                    item { Text("空。目标出了结果会自动进来。", style = MaterialTheme.typography.bodySmall) }
                }
                items(inbox, key = { it.id }) { item ->
                    Card(Modifier.fillMaxWidth()) {
                        Column(Modifier.padding(12.dp)) {
                            Text(
                                if (item.kind == "unassigned") "未归属的结果" else "新结果",
                                style = MaterialTheme.typography.labelSmall,
                            )
                            Text(item.title, style = MaterialTheme.typography.bodyMedium)
                            if (item.body.isNotEmpty()) {
                                Text(item.body, style = MaterialTheme.typography.bodySmall)
                            }
                            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
                                if (item.kind == "unassigned") {
                                    var menu by remember { mutableStateOf(false) }
                                    TextButton(onClick = { menu = true }) { Text("挂到目标") }
                                    DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                                        DropdownMenuItem(
                                            text = { Text("新建目标") },
                                            onClick = {
                                                menu = false
                                                scope.launch {
                                                    runCatching { client.post("/command/inbox/${item.id}/assign") }
                                                    message = "已新建目标"
                                                    refresh()
                                                }
                                            },
                                        )
                                        for (g in goals.filter { it.status == "open" }) {
                                            DropdownMenuItem(
                                                text = { Text("#${g.number} ${g.title.take(16)}") },
                                                onClick = {
                                                    menu = false
                                                    scope.launch {
                                                        runCatching {
                                                            client.post(
                                                                "/command/inbox/${item.id}/assign",
                                                                commandJson.parseToJsonElement(
                                                                    """{"goal_number":${g.number}}""",
                                                                ),
                                                            )
                                                        }
                                                        message = "已挂到目标 #${g.number}"
                                                        refresh()
                                                    }
                                                },
                                            )
                                        }
                                    }
                                }
                                TextButton(onClick = {
                                    scope.launch {
                                        runCatching { client.post("/command/inbox/${item.id}/done") }
                                        refresh()
                                    }
                                }) { Text("知道了") }
                            }
                        }
                    }
                }
                item {
                    Text(
                        "目标",
                        style = MaterialTheme.typography.titleSmall,
                        modifier = Modifier.padding(top = 12.dp, bottom = 4.dp),
                    )
                }
                if (goals.isEmpty()) {
                    item { Text("还没有目标。在电脑上的指挥台页面创建。", style = MaterialTheme.typography.bodySmall) }
                }
                items(goals.filter { it.status == "open" }, key = { it.number }) { g ->
                    val done = g.done.count { it.second }
                    Card(Modifier.fillMaxWidth().clickable { openGoal = g.number }) {
                        Column(Modifier.padding(12.dp)) {
                            Text("#${g.number} ${g.title}", style = MaterialTheme.typography.bodyMedium)
                            Text(
                                if (g.done.isEmpty()) "无完成标准" else "完成标准 $done/${g.done.size}",
                                style = MaterialTheme.typography.bodySmall,
                            )
                        }
                    }
                }
                message?.let { msg ->
                    item { Text(msg, style = MaterialTheme.typography.labelSmall) }
                }
            }
        }
    }

    // 目标详情：勾完成标准 + 复制开工包（设计文档：目标页只看进度和结果）
    openGoal?.let { n ->
        val g = goals.firstOrNull { it.number == n }
        if (g == null) {
            openGoal = null
            return
        }
        AlertDialog(
            onDismissRequest = { openGoal = null },
            confirmButton = { TextButton(onClick = { openGoal = null }) { Text("关闭") } },
            title = { Text("#${g.number} ${g.title}") },
            text = {
                Column {
                    g.done.forEachIndexed { i, (text, check) ->
                        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                            Checkbox(
                                checked = check,
                                onCheckedChange = { v ->
                                    scope.launch {
                                        runCatching {
                                            client.post(
                                                "/command/goals/$n/done",
                                                commandJson.parseToJsonElement(
                                                    """{"index":$i,"check":$v}""",
                                                ),
                                            )
                                        }
                                        refresh()
                                    }
                                },
                            )
                            Text(
                                text,
                                style = if (check) MaterialTheme.typography.bodySmall else MaterialTheme.typography.bodyMedium,
                            )
                        }
                    }
                    if (g.done.isEmpty()) Text("未填写完成标准", style = MaterialTheme.typography.bodySmall)
                    TextButton(onClick = {
                        scope.launch {
                            runCatching {
                                val el = client.get("/command/goals/$n/kickoff").jsonObject["text"]
                                val text = el?.jsonPrimitive?.content
                                if (text != null) {
                                    clipboard.setText(AnnotatedString(text))
                                    message = "开工包已复制，贴给任意工具"
                                }
                            }
                        }
                    }) { Text("复制开工包") }
                }
            },
        )
    }
    LaunchedEffect(message) {
        if (message != null) {
            delay(4000)
            message = null
        }
    }
}
