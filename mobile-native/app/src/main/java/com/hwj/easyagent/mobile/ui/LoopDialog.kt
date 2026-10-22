package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.size
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.ApiException
import com.hwj.easyagent.mobile.backend.LoopStatus
import kotlinx.coroutines.launch
import java.time.Duration
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter

private val intervals = listOf(5 to "5 分钟", 15 to "15 分钟", 30 to "30 分钟", 60 to "1 小时")
private val lifetimes = listOf(1 to "1 小时", 8 to "8 小时", 24 to "1 天", 72 to "3 天")

/**
 * Shows the session's /loop and lets the user stop it, or start one that
 * sends a prompt at a fixed interval (#250). Each trigger is a normal run, so
 * its result arrives like any other run (and as a notification in background).
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun LoopDialog(client: AgentClient, sessionId: String, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    var loading by remember { mutableStateOf(true) }
    var busy by remember { mutableStateOf(false) }
    var loop by remember { mutableStateOf<LoopStatus?>(null) }
    var problem by remember { mutableStateOf<String?>(null) }
    var prompt by remember { mutableStateOf("") }
    var minutes by remember { mutableIntStateOf(15) }
    var hours by remember { mutableIntStateOf(24) }

    fun act(block: suspend () -> Unit) {
        busy = true
        problem = null
        scope.launch {
            try {
                block()
            } catch (e: ApiException) {
                problem = e.message
            } catch (e: IllegalArgumentException) {
                problem = e.message
            } finally {
                busy = false
            }
        }
    }
    LaunchedEffect(sessionId) {
        try {
            loop = client.loop(sessionId)
        } catch (e: ApiException) {
            problem = e.message
        } finally {
            loading = false
        }
    }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("循环任务") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                val current = loop
                when {
                    loading -> CircularProgressIndicator(Modifier.size(24.dp))
                    current != null -> {
                        Text(current.prompt, style = MaterialTheme.typography.bodyLarge)
                        Text(
                            "每 ${formatInterval(current.intervalSeconds)} 执行一次 · ${remaining(current.expiresAt)}",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                        Text(
                            if (current.lastRunAt.isEmpty()) "还没有执行过" else "上次执行：" + clock(current.lastRunAt),
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                    else -> {
                        Text("按固定间隔向本会话发送同一条消息，适合定时巡检、盯部署。", style = MaterialTheme.typography.bodySmall)
                        OutlinedTextField(
                            value = prompt,
                            onValueChange = { prompt = it },
                            label = { Text("每次发送的内容") },
                            modifier = Modifier.fillMaxWidth(),
                            maxLines = 4,
                        )
                        Text("间隔", style = MaterialTheme.typography.labelMedium)
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            intervals.forEach { (value, label) ->
                                FilterChip(selected = minutes == value, onClick = { minutes = value }, label = { Text(label) })
                            }
                        }
                        Text("持续", style = MaterialTheme.typography.labelMedium)
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            lifetimes.forEach { (value, label) ->
                                FilterChip(selected = hours == value, onClick = { hours = value }, label = { Text(label) })
                            }
                        }
                        Text("服务重启后循环会停止，需要重新设置。", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
                problem?.let { Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall) }
            }
        },
        confirmButton = {
            when {
                loading -> {}
                loop != null -> TextButton(enabled = !busy, onClick = {
                    act {
                        client.stopLoop(sessionId)
                        loop = null
                    }
                }) { Text("停止循环") }
                else -> TextButton(enabled = !busy && prompt.isNotBlank(), onClick = {
                    act {
                        client.startLoop(sessionId, minutes, prompt, hours)
                        loop = client.loop(sessionId)
                    }
                }) { Text("开始") }
            }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("关闭") } },
    )
}

private fun formatInterval(seconds: Long): String = when {
    seconds % 3600 == 0L -> "${seconds / 3600} 小时"
    seconds % 60 == 0L -> "${seconds / 60} 分钟"
    else -> "$seconds 秒"
}

private fun remaining(expiresAt: String): String = runCatching {
    val left = Duration.between(OffsetDateTime.now(), OffsetDateTime.parse(expiresAt))
    if (left.isNegative) "已到期" else if (left.toHours() >= 1) "还剩 ${left.toHours()} 小时" else "还剩 ${left.toMinutes()} 分钟"
}.getOrDefault("")

private fun clock(time: String): String = runCatching {
    OffsetDateTime.parse(time).atZoneSameInstant(java.time.ZoneId.systemDefault()).format(DateTimeFormatter.ofPattern("MM-dd HH:mm"))
}.getOrDefault(time)
