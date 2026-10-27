package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Pause
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.ChatSession
import com.hwj.easyagent.mobile.backend.MessageQueue
import com.hwj.easyagent.mobile.backend.QueueItem

/**
 * 运行中的待发消息队列：展开逐条编辑、立即发送、移除，并可暂停/恢复整队，
 * 对应 POST /queue 的 edit / send_now / remove / pause / resume。
 */
@Composable
fun QueueCard(client: AgentClient, session: ChatSession, queue: MessageQueue) {
    var open by remember { mutableStateOf(false) }
    var editing by remember { mutableStateOf<QueueItem?>(null) }
    Surface(shape = RoundedCornerShape(12.dp), color = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.55f)) {
        Column(modifier = Modifier.fillMaxWidth()) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable { open = !open }
                    .padding(horizontal = 12.dp, vertical = 6.dp),
            ) {
                Icon(
                    if (queue.paused) Icons.Filled.Pause else Icons.Filled.PlayArrow,
                    contentDescription = null,
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(0.dp),
                )
                Text(
                    "排队中 ${queue.items.size} 条消息" + if (queue.paused) " · 队列已暂停" else "",
                    style = MaterialTheme.typography.labelLarge,
                )
                Spacer(Modifier.weight(1f))
                TextButton(onClick = { client.queueOp(session, if (queue.paused) "resume" else "pause") }) {
                    Text(if (queue.paused) "恢复队列" else "暂停队列")
                }
                Icon(if (open) Icons.Filled.ExpandLess else Icons.Filled.ExpandMore, contentDescription = null, modifier = Modifier.padding(0.dp))
            }
            if (open) {
                HorizontalDivider()
                queue.items.forEachIndexed { index, item ->
                    Row(
                        verticalAlignment = Alignment.CenterVertically,
                        modifier = Modifier.fillMaxWidth().padding(start = 12.dp, end = 4.dp, top = 2.dp, bottom = 2.dp),
                    ) {
                        Column(Modifier.weight(1f)) {
                            Text(
                                item.prompt.ifBlank { "（空消息）" },
                                style = MaterialTheme.typography.bodySmall,
                                maxLines = 2,
                                overflow = TextOverflow.Ellipsis,
                            )
                        }
                        Box {
                            var menu by remember { mutableStateOf(false) }
                            IconButton(onClick = { menu = true }) { Icon(Icons.Filled.MoreVert, contentDescription = "排队消息操作") }
                            DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                                DropdownMenuItem(text = { Text("编辑") }, onClick = { menu = false; editing = item })
                                DropdownMenuItem(text = { Text("立即发送") }, onClick = { menu = false; client.queueOp(session, "send_now", item.id) })
                                DropdownMenuItem(text = { Text("移除", color = MaterialTheme.colorScheme.error) }, onClick = { menu = false; client.queueOp(session, "remove", item.id) })
                            }
                        }
                    }
                    if (index < queue.items.lastIndex) HorizontalDivider()
                }
            }
        }
    }
    editing?.let { item ->
        var text by remember(item.id) { mutableStateOf(item.prompt) }
        AlertDialog(
            onDismissRequest = { editing = null },
            title = { Text("编辑排队消息") },
            text = {
                OutlinedTextField(
                    value = text,
                    onValueChange = { text = it },
                    maxLines = 6,
                    modifier = Modifier.fillMaxWidth(),
                )
            },
            confirmButton = {
                Button(
                    enabled = text.isNotBlank(),
                    onClick = {
                        client.queueOp(session, "edit", item.id, text.trim())
                        editing = null
                    },
                ) { Text("保存") }
            },
            dismissButton = { TextButton(onClick = { editing = null }) { Text("取消") } },
        )
    }
}
