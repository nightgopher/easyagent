package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.hwj.easyagent.mobile.backend.*
import kotlinx.serialization.json.JsonArray

@Composable
fun InboxDialog(client: AgentClient, onDismiss: () -> Unit, onOpen: (String) -> Unit) {
    var records by remember { mutableStateOf<JsonArray?>(null) }
    var error by remember { mutableStateOf("") }
    LaunchedEffect(client) {
        try { records = client.api.get("/inbox") as? JsonArray }
        catch (e: ApiException) { error = e.message.orEmpty() }
    }
    AlertDialog(onDismissRequest = onDismiss, title = { Text("收件箱 · ${client.config.label}") }, text = {
        LazyColumn(verticalArrangement = Arrangement.spacedBy(12.dp)) {
            if (error.isNotBlank()) item { Text(error) }
            if (records == null && error.isBlank()) item { CircularProgressIndicator() }
            if (records?.isEmpty() == true) item { Text("暂无收集的材料，可从其他 App 分享文字或链接。") }
            items(records.orEmpty()) { record ->
                val text = record.obj().getString("text")
                Column {
                    Text(text, maxLines = 8)
                    Text(record.obj().getString("source"), style = MaterialTheme.typography.labelSmall)
                    TextButton(onClick = {
                        client.createSession { id ->
                            client.chat(id).draft = "请根据以下材料整理成笔记：\n\n$text"
                            client.touch(); onDismiss(); onOpen(id)
                        }
                    }) { Text("带入新会话整理") }
                }
            }
        }
    }, confirmButton = { TextButton(onClick = onDismiss) { Text("关闭") } })
}
