@file:OptIn(ExperimentalMaterial3Api::class)

package com.hwj.easyagent.mobile.ui

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Autorenew
import androidx.compose.material.icons.filled.Build
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.ErrorOutline
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.filled.Psychology
import androidx.compose.material.icons.filled.Stop
import androidx.compose.material.icons.outlined.Shield
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledIconButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.ChatMessage
import com.hwj.easyagent.mobile.backend.Confirmation
import com.hwj.easyagent.mobile.backend.HistoryImage
import com.hwj.easyagent.mobile.backend.LinkStatus
import com.hwj.easyagent.mobile.backend.MessageQueue
import com.hwj.easyagent.mobile.backend.obj
import com.hwj.easyagent.mobile.backend.pretty
import com.hwj.easyagent.mobile.backend.str
import kotlinx.serialization.json.JsonElement

private sealed interface Entry {
    val key: String

    class Message(val message: ChatMessage) : Entry {
        override val key = "m${message.uid}"
    }

    class Tools(val messages: List<ChatMessage>) : Entry {
        override val key = "g${messages.first().uid}"
    }

    class Approval(val confirmation: Confirmation) : Entry {
        override val key = "c${confirmation.id}"
    }

    data object Queue : Entry {
        override val key = "queue"
    }
}

@Composable
fun ChatScreen(client: AgentClient, sessionId: String, onBack: () -> Unit) {
    BackHandler(onBack = onBack)
    LaunchedEffect(sessionId) { client.open(sessionId) }

    val rev by client.revision.collectAsStateWithLifecycle()
    val link by client.link.collectAsStateWithLifecycle()
    val sessions by client.sessions.collectAsStateWithLifecycle()
    val error by client.error.collectAsStateWithLifecycle()
    val session = remember(sessionId) { client.chat(sessionId) }
    val snackbar = remember { SnackbarHostState() }
    // Ask for notifications when the user first starts a task, the moment
    // results and approvals become worth notifying about (Android 13+).
    val context = LocalContext.current
    val notificationPermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) {}
    LaunchedEffect(session.sending) {
        if (session.sending && !NotificationPrompt.asked && Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            NotificationPrompt.asked = true
            notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }
    var loopDialog by remember { mutableStateOf(false) }
    if (loopDialog) LoopDialog(client, sessionId) { loopDialog = false }
    val title = sessions.firstOrNull { it.id == sessionId }?.displayTitle ?: "会话"

    LaunchedEffect(error) {
        error?.let {
            snackbar.showSnackbar(it)
            client.error.value = null
        }
    }

    // revision 驱动列表结构与会话状态，消息字段自身可观察；倒序让列表自然贴底。
    val entries = remember(rev, sessionId) {
        buildList<Entry> {
            toolRuns(session.messages).forEach { run ->
                add(if (run.size > 1) Entry.Tools(run) else Entry.Message(run.first()))
            }
            session.confirmations.forEach { add(Entry.Approval(it)) }
            if (session.queue.items.isNotEmpty()) add(Entry.Queue)
        }.asReversed()
    }
    val busy = session.running || session.pending
    val subtitle = session.statusText() + " · " + when (link) {
        LinkStatus.CONNECTED -> "已连接"
        LinkStatus.CONNECTING -> "连接中"
        LinkStatus.DISCONNECTED -> "已断开，正在重连"
    }

    Scaffold(
        modifier = Modifier.fillMaxSize(),
        topBar = {
            TopAppBar(
                title = {
                    Column {
                        Text(title, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text(
                            subtitle,
                            style = MaterialTheme.typography.labelSmall,
                            color = if (link == LinkStatus.CONNECTED) MaterialTheme.colorScheme.onSurfaceVariant else MaterialTheme.colorScheme.error,
                        )
                    }
                },
                navigationIcon = {
                    IconButton(onClick = onBack) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "返回") }
                },
                actions = {
                    IconButton(onClick = { loopDialog = true }) { Icon(Icons.Filled.Autorenew, contentDescription = "循环任务") }
                    if (busy) {
                        IconButton(onClick = { client.cancelRun(session) }) {
                            Icon(Icons.Filled.Stop, contentDescription = "停止当前任务", tint = MaterialTheme.colorScheme.error)
                        }
                    }
                },
            )
        },
        bottomBar = { MediaComposer(client, session, link, queued = busy) },
        snackbarHost = { SnackbarHost(snackbar) },
        contentWindowInsets = WindowInsets(0, 0, 0, 0),
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize()) {
            LazyColumn(
                modifier = Modifier.fillMaxSize(),
                reverseLayout = true,
                contentPadding = PaddingValues(horizontal = 12.dp, vertical = 10.dp),
                verticalArrangement = Arrangement.spacedBy(10.dp),
            ) {
                items(entries, key = { it.key }) { entry ->
                    when (entry) {
                        is Entry.Message -> MessageRow(
                            m = entry.message,
                            generating = session.running && entry.message === session.messages.lastOrNull(),
                            onToggle = { client.touch() },
                            images = { MessageImages(client, sessionId, it) },
                        )
                        is Entry.Tools -> ToolGroup(entry.messages) { client.touch() }
                        is Entry.Approval -> ApprovalCard(entry.confirmation) { approved ->
                            client.confirm(session, entry.confirmation.id, approved)
                        }
                        Entry.Queue -> QueueBanner(session.queue)
                    }
                }
            }
            if (entries.isEmpty()) {
                Text(
                    "发送第一条消息开始对话",
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.align(Alignment.Center),
                )
            }
        }
    }
}

@Composable
private fun MessageRow(m: ChatMessage, generating: Boolean, onToggle: () -> Unit, images: @Composable (List<HistoryImage>) -> Unit) {
    when {
        m.user -> Column(horizontalAlignment = Alignment.End, modifier = Modifier.fillMaxWidth()) {
            if (m.images.isNotEmpty()) {
                images(m.images)
                Spacer(Modifier.height(6.dp))
            }
            if (m.text.isNotBlank() && m.text != "[图片]".repeat(m.images.size)) UserBubble(m.text)
        }
        m.kind == "tool" -> ToolCard(m, onToggle)
        m.kind == "thought" -> ThoughtCard(m, generating, onToggle)
        m.kind == "error" -> ErrorCard(m.text)
        m.kind == "compaction" -> Note("上下文已压缩", m.text)
        m.isWaiting -> WaitingRow()
        else -> MarkdownView(m.text, Modifier.fillMaxWidth())
    }
}

@Composable
private fun UserBubble(text: String) {
    Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
        Surface(
            color = MaterialTheme.colorScheme.primaryContainer,
            contentColor = MaterialTheme.colorScheme.onPrimaryContainer,
            shape = RoundedCornerShape(18.dp, 18.dp, 4.dp, 18.dp),
            modifier = Modifier.widthIn(max = 320.dp),
        ) {
            SelectionContainer {
                Text(text, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.padding(horizontal = 14.dp, vertical = 10.dp))
            }
        }
    }
}

@Composable
private fun ThoughtCard(m: ChatMessage, generating: Boolean, onToggle: () -> Unit) {
    val expanded = if (generating && !m.expansionTouched) true else m.expanded
    Card(
        onClick = {
            m.expanded = !expanded
            m.expansionTouched = true
            onToggle()
        },
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.55f)),
    ) {
        Row(
            modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Icon(
                Icons.Filled.Psychology,
                contentDescription = null,
                tint = if (generating) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.size(18.dp),
            )
            Text("思考过程", style = MaterialTheme.typography.labelLarge)
            Spacer(Modifier.weight(1f))
            val words = m.text.trim().length
            Text(
                when {
                    generating -> "正在思考…"
                    words > 0 -> "已深度思考 · $words 字"
                    else -> "深度思考"
                },
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Icon(if (expanded) Icons.Filled.ExpandLess else Icons.Filled.ExpandMore, contentDescription = null, modifier = Modifier.size(18.dp))
        }
        if (expanded) {
            val body = m.text.ifBlank { if (generating) "思考中…" else "" }
            if (body.isNotEmpty()) {
                SelectionContainer {
                    Text(
                        body,
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(start = 12.dp, end = 12.dp, bottom = 12.dp),
                    )
                }
            }
        }
    }
}

@Composable
private fun ToolCard(m: ChatMessage, onToggle: () -> Unit) {
    val clipboard = LocalClipboardManager.current
    val failed = m.status == "失败" || m.status == "未完成"
    val statusColor = when {
        failed -> MaterialTheme.colorScheme.error
        m.status == "已拒绝" -> MaterialTheme.colorScheme.tertiary
        else -> MaterialTheme.colorScheme.onSurfaceVariant
    }
    Card(
        onClick = {
            m.expanded = !m.expanded
            m.expansionTouched = true
            onToggle()
        },
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceVariant),
    ) {
        Row(
            modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            ToolStatusIcon(m.status, statusColor)
            Text(m.tool, style = MaterialTheme.typography.labelLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(
                toolDescription(m),
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f),
            )
            val duration = if (m.durationMs > 0) " · %.1fs".format(m.durationMs / 1000) else ""
            Text(m.status + duration, style = MaterialTheme.typography.labelSmall, color = statusColor)
        }
        if (m.expanded) {
            Column(
                modifier = Modifier.padding(start = 12.dp, end = 12.dp, bottom = 12.dp),
                verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                m.args?.let {
                    SectionLabel("调用参数")
                    CodeBlock(truncate(pretty(it)), modifier = Modifier.fillMaxWidth())
                }
                val result = toolResult(m)
                if (result.isNotEmpty()) {
                    SectionLabel("输出")
                    CodeBlock(truncate(ansiSequence.replace(result, "")), modifier = Modifier.fillMaxWidth())
                    TextButton(onClick = { clipboard.setText(AnnotatedString(result)) }) {
                        Icon(Icons.Filled.ContentCopy, contentDescription = null, modifier = Modifier.size(16.dp))
                        Spacer(Modifier.width(6.dp))
                        Text("复制输出")
                    }
                }
                m.details?.takeIf { it.isNotEmpty() }?.let {
                    SectionLabel("执行详情")
                    CodeBlock(truncate(pretty(it)), modifier = Modifier.fillMaxWidth())
                }
            }
        }
    }
}

@Composable
private fun ToolStatusIcon(status: String, color: Color) {
    when (status) {
        "执行中" -> CircularProgressIndicator(modifier = Modifier.size(16.dp), strokeWidth = 2.dp)
        "完成" -> Icon(Icons.Filled.Check, contentDescription = null, tint = MaterialTheme.colorScheme.primary, modifier = Modifier.size(18.dp))
        "失败", "未完成" -> Icon(Icons.Filled.ErrorOutline, contentDescription = null, tint = color, modifier = Modifier.size(18.dp))
        "已拒绝" -> Icon(Icons.Outlined.Shield, contentDescription = null, tint = color, modifier = Modifier.size(18.dp))
        else -> Icon(Icons.Filled.Build, contentDescription = null, tint = color, modifier = Modifier.size(18.dp))
    }
}

/** Consecutive tool calls folded into one row; failures open it until the user decides. */
@Composable
private fun ToolGroup(messages: List<ChatMessage>, onToggle: () -> Unit) {
    var touched by rememberSaveable { mutableStateOf(false) }
    var open by rememberSaveable { mutableStateOf(false) }
    val failed = messages.count { it.status == "失败" || it.status == "未完成" }
    val expanded = if (touched) open else failed > 0
    val running = messages.lastOrNull { it.status == "执行中" }
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Surface(
            onClick = {
                open = !expanded
                touched = true
                onToggle()
            },
            shape = RoundedCornerShape(12.dp),
            color = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.55f),
        ) {
            Row(
                modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                if (running != null) CircularProgressIndicator(modifier = Modifier.size(16.dp), strokeWidth = 2.dp)
                else Icon(Icons.Filled.Build, contentDescription = null, tint = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.size(18.dp))
                Text(
                    if (running != null) "正在执行 ${running.tool}" else "已调用 ${messages.size} 个工具",
                    style = MaterialTheme.typography.labelLarge,
                )
                Text(
                    toolNames(messages),
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f),
                )
                if (failed > 0) Text("$failed 个失败", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.error)
                Icon(if (expanded) Icons.Filled.ExpandLess else Icons.Filled.ExpandMore, contentDescription = null, modifier = Modifier.size(18.dp))
            }
        }
        if (expanded) messages.forEach { ToolCard(it, onToggle) }
    }
}

/**
 * Asks before a tool runs. The request reads as one line; arguments fold away,
 * and only shell commands, which can do anything, are drawn as a warning.
 */
@Composable
private fun ApprovalCard(c: Confirmation, onDecide: (Boolean) -> Unit) {
    var showArgs by rememberSaveable(c.id) { mutableStateOf(false) }
    val risky = c.toolName == "bash"
    val accent = if (risky) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.primary
    val summary = c.description.ifEmpty { argumentSummary(c.args) }
    Card(
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceVariant),
        border = BorderStroke(1.dp, if (risky) accent else MaterialTheme.colorScheme.outlineVariant),
    ) {
        Column(modifier = Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Icon(Icons.Outlined.Shield, contentDescription = null, tint = accent, modifier = Modifier.size(18.dp))
                Text("需要审批", style = MaterialTheme.typography.labelLarge, color = accent)
                Text(
                    c.toolName.ifEmpty { "工具调用" },
                    style = MaterialTheme.typography.labelLarge,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            if (summary.isNotEmpty()) Text(summary, style = MaterialTheme.typography.bodyMedium, maxLines = 3, overflow = TextOverflow.Ellipsis)
            if (showArgs) c.args?.let { CodeBlock(truncate(pretty(it), 1500), modifier = Modifier.fillMaxWidth()) }
            Row(modifier = Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (c.args != null) {
                    TextButton(onClick = { showArgs = !showArgs }) { Text(if (showArgs) "收起参数" else "查看参数") }
                }
                Spacer(Modifier.weight(1f))
                OutlinedButton(onClick = { onDecide(false) }) { Text("拒绝") }
                Button(onClick = { onDecide(true) }) { Text("批准") }
            }
        }
    }
}

@Composable
private fun QueueBanner(queue: MessageQueue) {
    Surface(shape = RoundedCornerShape(10.dp), color = MaterialTheme.colorScheme.surfaceVariant) {
        Text(
            "排队中 ${queue.items.size} 条消息" + if (queue.paused) " · 队列已暂停" else "",
            style = MaterialTheme.typography.labelMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(horizontal = 12.dp, vertical = 8.dp),
        )
    }
}

@Composable
private fun ErrorCard(text: String) {
    Card(colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.errorContainer)) {
        SelectionContainer {
            Text(
                text.ifBlank { "发生未知错误" },
                color = MaterialTheme.colorScheme.onErrorContainer,
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.padding(12.dp),
            )
        }
    }
}

@Composable
private fun Note(label: String, text: String) {
    Column(modifier = Modifier.fillMaxWidth().padding(horizontal = 4.dp)) {
        Text(label, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        if (text.isNotBlank()) {
            Text(text, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 6, overflow = TextOverflow.Ellipsis)
        }
    }
}

@Composable
private fun WaitingRow() {
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        CircularProgressIndicator(modifier = Modifier.size(14.dp), strokeWidth = 2.dp)
        Text("生成中…", style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}

@Composable
private fun SectionLabel(text: String) {
    Text(text, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
}

private val ansiSequence = Regex("\u001B\\[[0-?]*[ -/]*[@-~]")

private fun toolDescription(m: ChatMessage): String = argumentSummary(m.args)

private fun argumentSummary(arguments: JsonElement?): String {
    val args = arguments.obj()
    for (key in listOf("command", "path", "file_path", "pattern", "query", "url")) {
        val text = args[key].str()
        if (text.isNotEmpty()) return text
    }
    return ""
}

/** Splits messages into runs: consecutive tool calls together, everything else alone. */
internal fun toolRuns(messages: List<ChatMessage>): List<List<ChatMessage>> {
    val runs = ArrayList<MutableList<ChatMessage>>()
    for (m in messages) {
        val last = runs.lastOrNull()
        if (m.kind == "tool" && last != null && last.last().kind == "tool") last += m else runs += mutableListOf(m)
    }
    return runs
}

/** "bash ×2 · read", in order of first use. */
internal fun toolNames(messages: List<ChatMessage>): String =
    messages.groupingBy { it.tool }.eachCount().entries.joinToString(" · ") { (name, n) -> if (n > 1) "$name ×$n" else name }

private fun toolResult(m: ChatMessage): String {
    var result = m.result
    if (result.isEmpty() && m.args == null) result = m.text
    if (result.isEmpty() && m.status == "未完成") result = "此工具调用未完成或已中断"
    return result
}

private fun truncate(text: String, limit: Int = 6000): String =
    if (text.length <= limit) text else text.take(limit) + "\n…（已截断，共 ${text.length} 字）"

/** Asks for the notification permission at most once per process. */
private object NotificationPrompt {
    var asked = false
}
