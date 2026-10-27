package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.hwj.easyagent.mobile.backend.*
import kotlinx.serialization.json.JsonArray
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

@Composable
fun NewSessionDialog(client: AgentClient, onDismiss: () -> Unit, onOpen: (String) -> Unit) {
    val sessions by client.sessions.collectAsState()
    var cwd by rememberSaveable { mutableStateOf("") }
    var model by rememberSaveable { mutableStateOf("") }
    var models by remember { mutableStateOf<List<String>>(emptyList()) }
    var error by remember { mutableStateOf("") }
    var loadingModels by remember { mutableStateOf(true) }
    var creating by remember { mutableStateOf(false) }
    LaunchedEffect(client) {
        // Keep snapshot writes on the UI dispatcher even when the effect context is unconfined.
        withContext(Dispatchers.Main.immediate) {
            try { models = (client.api.get("/models").obj()["models"] as? JsonArray).orEmpty().map { it.obj().getString("id") }.distinct() }
            catch (e: ApiException) { error = e.message.orEmpty() }
            finally { loadingModels = false }
        }
    }
    // Failure lives in the shared error flow; permit a retry without losing choices.
    val failure by client.error.collectAsState()
    LaunchedEffect(failure) { if (failure != null) creating = false }
    AlertDialog(onDismissRequest = { if (!creating) onDismiss() }, title = { Text("新会话 · ${client.config.label}") }, text = {
        Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("默认全权限：允许主机在已有工具能力内自动执行。")
            OutlinedTextField(cwd, { cwd = it }, label = { Text("项目目录（留空使用主机默认）") })
            sessions.map { it.workspace }.filter { it.isNotBlank() }.distinct().take(8).forEach { path ->
                TextButton(onClick = { cwd = path }) { Text(path) }
            }
            Text("模型：${model.ifBlank { "主机默认" }}")
            TextButton(onClick = { model = "" }) { Text("使用默认模型") }
            if (loadingModels) Text("正在加载模型…")
            models.forEach { id -> TextButton(onClick = { model = id }) { Text(id) } }
            if (error.isNotBlank()) Text("模型列表不可用，将使用主机默认：$error")
        }
    }, confirmButton = { Button(enabled = !creating, onClick = {
        creating = true
        client.createSession(cwd, model) { onDismiss(); onOpen(it) }
    }) { Text(if (creating) "创建中…" else "创建") } }, dismissButton = { TextButton(enabled = !creating, onClick = onDismiss) { Text("取消") } })
}
