package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.ApiException
import com.hwj.easyagent.mobile.backend.ChatSession
import com.hwj.easyagent.mobile.backend.ModelsResponse

/** 从 GET /models 选择并切换本会话的模型；切换失败（如任务运行中）以 snackbar 提示。 */
@Composable
fun ModelPickerDialog(client: AgentClient, session: ChatSession, onDismiss: () -> Unit) {
    var loading by remember { mutableStateOf(true) }
    var result by remember { mutableStateOf<ModelsResponse?>(null) }
    var problem by remember { mutableStateOf<String?>(null) }
    LaunchedEffect(Unit) {
        try {
            result = client.models()
        } catch (e: ApiException) {
            problem = e.message
        } finally {
            loading = false
        }
    }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("切换模型") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                when {
                    loading -> Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.Center) {
                        CircularProgressIndicator(Modifier.padding(16.dp))
                    }
                    problem != null -> Text(problem!!, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.error)
                    result?.models.isNullOrEmpty() -> Text(
                        "主机没有发现可用模型",
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    else -> {
                        val response = result!!
                        LazyColumn(modifier = Modifier.heightIn(max = 420.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                            items(response.models, key = { it.id }) { model ->
                                Row(
                                    verticalAlignment = Alignment.CenterVertically,
                                    horizontalArrangement = Arrangement.spacedBy(10.dp),
                                    modifier = Modifier
                                        .fillMaxWidth()
                                        .clickable {
                                            client.switchModel(session, model)
                                            onDismiss()
                                        }
                                        .padding(horizontal = 4.dp, vertical = 6.dp),
                                ) {
                                    RadioButton(selected = model.id == response.current?.id, onClick = null)
                                    Column(Modifier.weight(1f)) {
                                        Text(model.name.ifBlank { model.id }, style = MaterialTheme.typography.bodyMedium)
                                        if (model.name != model.id && model.id.isNotBlank()) {
                                            Text(
                                                model.id,
                                                style = MaterialTheme.typography.labelSmall,
                                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                            )
                                        }
                                    }
                                    if (model.contextWindow > 0) {
                                        Text(
                                            "${model.contextWindow / 1024}K",
                                            style = MaterialTheme.typography.labelSmall,
                                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                                        )
                                    }
                                }
                            }
                        }
                        response.discoveryError.takeIf { it.isNotEmpty() }?.let {
                            Text("发现模型时出错：$it", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.error)
                        }
                    }
                }
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text("关闭") } },
    )
}
