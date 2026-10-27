package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.hwj.easyagent.mobile.backend.*
import com.hwj.easyagent.mobile.media.SharedCapture
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.*
import java.io.File
import java.util.Base64

@Composable
fun ShareReview(capture: SharedCapture, client: AgentClient, onDone: () -> Unit, onOpen: (String) -> Unit) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var text by rememberSaveable(capture.id) { mutableStateOf(capture.text) }
    var sessionId by rememberSaveable(capture.id, client.config.id) { mutableStateOf(if (capture.host == client.storageIdentity) capture.session else "") }
    var uploaded by rememberSaveable(capture.id, client.config.id) { mutableStateOf(if (capture.host == client.storageIdentity) capture.uploaded else emptyMap()) }
    suspend fun persist() = withContext(Dispatchers.IO) {
        com.hwj.easyagent.mobile.media.saveShare(context, capture.copy(text = text, host = client.storageIdentity, session = sessionId, uploaded = uploaded))
    }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    fun later() {
        if (busy) return
        busy = true
        scope.launch {
            try { persist(); onDone() }
            catch (e: Exception) { if (e is kotlinx.coroutines.CancellationException) throw e; error = e.message.orEmpty() }
            finally { busy = false }
        }
    }
    AlertDialog(onDismissRequest = ::later, title = { Text("接收分享 · ${client.config.label}") }, text = {
        Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("内容已暂存到本机。确认后才上传到当前主机；如需换主机，请先返回会话列表。")
            OutlinedTextField(text, { text = it }, enabled = !busy, label = { Text("分享内容或处理要求") })
            capture.files.forEach { Text(it.name) }
            capture.errors.forEach { Text(it, color = MaterialTheme.colorScheme.error) }
            Text("文件可发送到会话；纯文本也可存入主机收件箱。")
            if (error.isNotEmpty()) Text(error, color = MaterialTheme.colorScheme.error)
            TextButton(enabled = !busy, onClick = {
                busy = true
                scope.launch {
                    withContext(Dispatchers.IO) { com.hwj.easyagent.mobile.media.removeShare(context, capture) }
                    onDone()
                }
            }) { Text("丢弃分享") }
        }
    }, dismissButton = { TextButton(enabled = !busy, onClick = ::later) { Text("稍后处理") } }, confirmButton = {
        Column {
            TextButton(enabled = !busy && text.isNotBlank() && capture.files.isEmpty(), onClick = {
                busy = true
                scope.launch {
                    try {
                        client.api.post("/inbox", buildJsonObject { put("id", capture.id); put("text", text); put("source", "Android 分享") })
                        withContext(Dispatchers.IO) { com.hwj.easyagent.mobile.media.removeShare(context, capture) }
                        onDone()
                    } catch (e: ApiException) { error = e.message.orEmpty() }
                    finally { busy = false }
                }
            }) { Text("存入收件箱") }
            Button(enabled = !busy && (text.isNotBlank() || capture.files.isNotEmpty()), onClick = {
                busy = true
                scope.launch {
                    try {
                        if (sessionId.isEmpty()) {
                            sessionId = client.api.post("/sessions", buildJsonObject {}).obj().getString("id")
                            if (sessionId.isEmpty()) throw ApiException("服务未返回会话 ID")
                            persist()
                        }
                        val mode = client.api.post(ApiClient.sessionPath(sessionId) + "/permissions", buildJsonObject { put("mode", "full") })
                        val session = client.chat(sessionId)
                        session.accessMode = mode.obj().getString("access_mode")
                        // Keep the durable capture until all uploads succeed. Existing uploads are reused on retry.
                        capture.files.forEach { file ->
                            val cached = uploaded[file.storage]?.let { saved ->
                                runCatching { wireJson.decodeFromString(UploadedAttachment.serializer(), saved) }.getOrNull()
                            }
                            if (cached != null) {
                                if (session.attachments.none { it.attachment.id == cached.id }) session.attachments = session.attachments + PendingAttachment(cached, null)
                            } else {
                                val bytes = withContext(Dispatchers.IO) { File(context.filesDir, "shares/${capture.id}/${file.storage}").readBytes() }
                                val response = client.api.post(ApiClient.sessionPath(sessionId) + "/attachments", buildJsonObject {
                                    put("name", file.name); put("data", Base64.getEncoder().encodeToString(bytes))
                                })
                                val attachment = wireJson.decodeFromJsonElement(UploadedAttachment.serializer(), response)
                                if (attachment.id.isEmpty()) throw ApiException("服务未返回附件 ID")
                                session.attachments = session.attachments + PendingAttachment(attachment, null)
                                client.touch()
                                uploaded = uploaded + (file.storage to wireJson.encodeToString(UploadedAttachment.serializer(), attachment))
                                persist()
                            }
                        }
                        val shareText = text.ifBlank { "请分析这些附件" }
                        if (session.draft.isBlank()) session.draft = shareText
                        else if (session.draft != shareText) session.draft += "\n\n" + shareText
                        client.touch()
                        withContext(Dispatchers.IO) { com.hwj.easyagent.mobile.media.removeShare(context, capture) }
                        client.refreshSessions()
                        onDone(); onOpen(sessionId)
                    } catch (e: Exception) {
                        if (e is kotlinx.coroutines.CancellationException) throw e
                        error = e.message ?: "处理失败，可重试"
                    } finally { busy = false }
                }
            }) { Text(if (busy) "处理中…" else "带入新会话") }
        }
    })
}
