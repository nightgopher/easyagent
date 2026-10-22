package com.hwj.easyagent.mobile.backend

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.job
import kotlinx.coroutines.launch
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.security.SecureRandom
import java.util.Base64

enum class LinkStatus { CONNECTING, CONNECTED, DISCONNECTED }

/**
 * 一台运行主机的连接：维护 WebSocket 订阅、重连与各会话的投影状态。
 * 所有状态只在主线程修改；UI 通过 [revision] 感知变化后直接读取。
 */
class AgentClient(val config: HostConfig) {
    val api = ApiClient(config.url, requireNotNull(config.token))
    val link = MutableStateFlow(LinkStatus.CONNECTING)
    val sessions = MutableStateFlow<List<SessionSummary>>(emptyList())
    val sessionsLoading = MutableStateFlow(false)
    val error = MutableStateFlow<String?>(null)
    val activeId = MutableStateFlow<String?>(null)
    val revision = MutableStateFlow(0L)

    /** Receives approvals and finished runs; set by the app to post notifications. */
    var onAlert: ((AgentClient, Alert) -> Unit)? = null

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val chats = HashMap<String, ChatSession>()
    private var socket: WebSocket? = null
    private var reconnectJob: Job? = null
    private var backoffMs = 1_000L
    @Volatile private var closed = false

    val activeChat: ChatSession? get() = activeId.value?.let { chats[it] }

    fun chat(id: String): ChatSession = chats.getOrPut(id) { ChatSession(id) }

    fun start() {
        connect()
        refreshSessions()
        scope.launch {
            while (isActive) {
                delay(60_000)
                if (link.value == LinkStatus.CONNECTED) refreshSessions()
            }
        }
    }

    /** 回到前台：立即重连并刷新会话列表。 */
    fun resume() {
        if (closed) return
        refreshSessions()
        if (link.value == LinkStatus.DISCONNECTED) {
            reconnectJob?.cancel()
            backoffMs = 1_000L
            connect()
        }
    }

    @Synchronized fun shutdown() {
        closed = true
        reconnectJob?.cancel()
        socket?.cancel()
        socket = null
        scope.cancel()
        api.close()
        link.value = LinkStatus.DISCONNECTED
    }

    /** Tests and other lifecycle owners can await cancelled in-flight requests. */
    internal suspend fun awaitShutdown() { scope.coroutineContext.job.join() }

    fun touch() {
        revision.value++
    }

    private fun connect() {
        if (closed) return
        link.value = LinkStatus.CONNECTING
        socket = api.openSocket(object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) = post {
                if (socket !== webSocket) return@post
                link.value = LinkStatus.CONNECTED
                backoffMs = 1_000L
                error.value = null
                activeChat?.let(::subscribe)
            }

            override fun onMessage(webSocket: WebSocket, text: String) = post {
                if (socket !== webSocket) return@post
                runCatching { wireJson.decodeFromString(Envelope.serializer(), text) }.getOrNull()?.let(::receive)
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = post { dropped(webSocket) }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) = post {
                webSocket.close(code, reason)
                dropped(webSocket)
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = post { dropped(webSocket) }
        })
    }

    private fun dropped(webSocket: WebSocket) {
        if (closed || socket !== webSocket) return
        socket = null
        link.value = LinkStatus.DISCONNECTED
        chats.values.forEach { it.sending = false }
        touch()
        reconnectJob?.cancel()
        reconnectJob = scope.launch {
            delay(backoffMs)
            backoffMs = minOf(backoffMs * 2, 15_000L)
            connect()
        }
    }

    @Synchronized private fun post(block: () -> Unit) {
        if (closed) return
        scope.launch { block() }
    }

    private fun send(value: JsonObject): Boolean {
        val current = socket ?: return false
        return current.send(wireJson.encodeToString(JsonObject.serializer(), value))
    }

    private fun subscribe(s: ChatSession) {
        send(buildJsonObject {
            put("type", "subscribe")
            put("session_id", s.id)
            put("after_seq", s.seq)
            put("run_id", s.runId)
        })
    }

    private fun receive(e: Envelope) {
        val s = chats[e.sessionId] ?: return
        val before = s.alertState()
        val pendingMatched = (e.type == "accepted" && e.requestId == s.pendingRequest) ||
            (e.type == "snapshot" && e.run != null && e.run.requestId == s.pendingRequest)
        if (s.pendingRequest != "" && pendingMatched) {
            s.sending = false
            s.pendingRequest = ""
            acknowledgeDraft(s)
        }
        if (e.type == "error" && e.requestId == s.pendingRequest) {
            s.sending = false
            error.value = e.message
        }
        s.reduce(e)
        onAlert?.let { notify -> alertsFor(before, s).forEach { notify(this, it) } }
        if (e.type == "status" && !s.running) refreshSessions()
        touch()
    }

    /** Session title for notifications, falling back to a short ID. */
    fun titleOf(sessionId: String): String =
        sessions.value.firstOrNull { it.id == sessionId }?.title?.takeIf { it.isNotBlank() } ?: "会话 " + sessionId.take(8)

    fun open(id: String) {
        activeId.value = id
        val s = chat(id)
        touch()
        reload(s)
    }

    private fun reload(s: ChatSession) {
        scope.launch {
            try {
                val raw = api.get(ApiClient.sessionPath(s.id) + "/run")
                val envelope = wireJson.decodeFromJsonElement(Envelope.serializer(), raw)
                receive(if (envelope.sessionId.isEmpty()) envelope.copy(sessionId = s.id) else envelope)
                if (link.value == LinkStatus.CONNECTED) subscribe(s)
            } catch (e: ApiException) {
                error.value = e.message
            }
        }
    }

    fun sendPrompt(s: ChatSession, text: String) {
        val trimmed = text.trim()
        if (trimmed.isEmpty() || s.sending) return
        if (link.value != LinkStatus.CONNECTED) {
            error.value = "尚未连接，草稿已保留"
            return
        }
        if (s.uploading > 0) {
            error.value = "附件仍在上传，请稍候"
            return
        }
        val inputs = buildJsonObject {
            put("attachments", JsonArray(s.attachments.map { JsonPrimitive(it.attachment.id) }))
            put("files", JsonArray(emptyList()))
        }
        val attachments = s.attachments.map { it.attachment.id }.toSet()
        if (s.pendingRequest == "" || s.sentDraft != text || s.sentAttachments != attachments) s.pendingRequest = requestId()
        s.sentAttachments = attachments
        s.sentDraft = text
        val request = s.pendingRequest
        s.sending = true
        touch()
        if (s.running || s.pending) {
            val body = buildJsonObject {
                put("action", "add")
                put("id", request)
                put("prompt", trimmed)
                put("inputs", inputs)
            }
            scope.launch {
                try {
                    val result = api.post(ApiClient.sessionPath(s.id) + "/queue", body)
                    runCatching { wireJson.decodeFromJsonElement(MessageQueue.serializer(), result) }.getOrNull()?.let {
                        if (it.revision >= s.queue.revision) s.queue = it
                    }
                    if (s.pendingRequest == request) {
                        s.pendingRequest = ""
                        acknowledgeDraft(s)
                        s.sending = false
                    }
                } catch (e: ApiException) {
                    error.value = e.message
                    if (s.pendingRequest == request) s.sending = false
                }
                touch()
            }
            return
        }
        val ok = send(buildJsonObject {
            put("type", "prompt")
            put("session_id", s.id)
            put("request_id", request)
            put("prompt", trimmed)
            put("inputs", inputs)
        })
        if (!ok) {
            s.sending = false
            error.value = "连接已断开，草稿已保留"
            touch()
            return
        }
        touch()
        scope.launch {
            delay(12_000)
            if (s.sending && s.pendingRequest == request) {
                s.sending = false
                error.value = "服务尚未确认接收，草稿已保留；可重试，重复请求不会重复执行"
                touch()
            }
        }
    }

    fun cancelRun(s: ChatSession) {
        call(ApiClient.sessionPath(s.id) + "/run/cancel", buildJsonObject { put("run_id", s.runId) }) { reload(s) }
    }

    fun confirm(s: ChatSession, confirmationId: String, approved: Boolean) {
        val body = buildJsonObject {
            put("run_id", s.runId)
            put("confirmation_id", confirmationId)
            put("approved", approved)
        }
        call(ApiClient.sessionPath(s.id) + "/run/confirm", body) { reload(s) }
    }

    /** Answers an approval from outside the chat screen, e.g. a notification action. */
    fun confirmById(sessionId: String, confirmationId: String, approved: Boolean) {
        confirm(chat(sessionId), confirmationId, approved)
    }

    /** The session's active loop, or null when none runs. */
    suspend fun loop(sessionId: String): LoopStatus? {
        val raw = api.get(ApiClient.sessionPath(sessionId) + "/loop").obj()["loop"]
        if (raw == null || raw is JsonNull) return null
        return wireJson.decodeFromJsonElement(LoopStatus.serializer(), raw)
    }

    /**
     * Starts a loop through the host's /loop command, so it behaves exactly as
     * in the TUI. Returns the command's reply; throws if the host refused.
     */
    suspend fun startLoop(sessionId: String, minutes: Int, prompt: String, expiresHours: Int): String {
        val text = prompt.replace(Regex("\\s+"), " ").trim()
        require(text.isNotEmpty()) { "循环内容不能为空" }
        val command = "/loop ${minutes}m $text --expires ${expiresHours}h"
        val raw = api.post(ApiClient.sessionPath(sessionId) + "/command", buildJsonObject { put("command", command) })
        val output = raw.obj().getString("output")
        if (loop(sessionId) == null) throw ApiException(output.ifBlank { "循环任务没有启动" })
        return output
    }

    suspend fun stopLoop(sessionId: String) {
        api.delete(ApiClient.sessionPath(sessionId) + "/loop")
    }

    fun refreshSessions() {
        if (sessionsLoading.value) return
        sessionsLoading.value = true
        scope.launch {
            try {
                val raw = api.get("/sessions")
                val list = wireJson.decodeFromJsonElement(ListSerializer(SessionSummary.serializer()), raw)
                sessions.value = list.filter { it.id.isNotEmpty() }.sortedByDescending { it.pinned }
            } catch (e: ApiException) {
                error.value = e.message
            } finally {
                sessionsLoading.value = false
            }
        }
    }

    fun createSession(onCreated: (String) -> Unit) {
        scope.launch {
            try {
                val raw = api.post("/sessions", buildJsonObject {})
                val id = raw.obj().getString("id")
                if (id.isEmpty()) throw ApiException("服务未返回会话 ID")
                refreshSessions()
                onCreated(id)
            } catch (e: ApiException) {
                error.value = e.message
            }
        }
    }

    fun deleteSession(id: String) {
        scope.launch {
            try {
                api.delete(ApiClient.sessionPath(id))
                chats.remove(id)
                if (activeId.value == id) activeId.value = null
                refreshSessions()
            } catch (e: ApiException) {
                error.value = e.message
            }
        }
    }

    fun patchSession(id: String, patch: JsonObject) {
        call(ApiClient.sessionPath(id), patch, method = "PATCH") { refreshSessions() }
    }

    private fun call(path: String, body: JsonObject, method: String = "POST", done: () -> Unit) {
        scope.launch {
            try {
                if (method == "PATCH") api.patch(path, body) else api.post(path, body)
                done()
            } catch (e: ApiException) {
                error.value = e.message
            }
        }
    }

    private fun requestId(): String {
        val bytes = ByteArray(16)
        SecureRandom().nextBytes(bytes)
        return "mobile-" + bytes.joinToString("") { "%02x".format(it) }
    }

    private fun acknowledgeDraft(s: ChatSession) {
        if (s.draft == s.sentDraft) s.draft = ""
        s.attachments = s.attachments.filterNot { it.attachment.id in s.sentAttachments }
        s.sentAttachments = emptySet()
    }

    /** Uploads one attachment for the session's next prompt (server limit 4 MiB). */
    fun uploadAttachment(s: ChatSession, name: String, bytes: ByteArray, preview: ByteArray?) {
        if (bytes.size > MAX_ATTACHMENT_BYTES) {
            error.value = "「$name」超过 4 MiB，无法上传"
            return
        }
        s.uploading++
        touch()
        scope.launch {
            try {
                val body = buildJsonObject {
                    put("name", name)
                    put("data", Base64.getEncoder().encodeToString(bytes))
                }
                val raw = api.post(ApiClient.sessionPath(s.id) + "/attachments", body)
                val uploaded = wireJson.decodeFromJsonElement(UploadedAttachment.serializer(), raw)
                if (uploaded.id.isEmpty()) throw ApiException("服务未返回附件 ID")
                s.attachments = s.attachments + PendingAttachment(uploaded, preview)
            } catch (e: ApiException) {
                error.value = "「$name」上传失败：" + e.message
            } finally {
                s.uploading--
                touch()
            }
        }
    }

    fun removeAttachment(s: ChatSession, id: String) {
        s.attachments = s.attachments.filter { it.attachment.id != id }
        touch()
    }

    /** Sends a WAV recording to the host's ASR proxy and returns the text. */
    suspend fun transcribe(wav: ByteArray): String {
        val raw = api.postFile("/asr/transcribe", "file", "voice.wav", "audio/wav", wav)
        return raw.obj().getString("text").trim()
    }

    private val images = object : LinkedHashMap<String, ByteArray>(16, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, ByteArray>?) = size > 24
    }

    /** Bytes of an image in a message, cached for the most recent 24 images. */
    suspend fun image(sessionId: String, image: HistoryImage): ByteArray {
        if (image.dataUrl.isNotEmpty()) {
            val encoded = image.dataUrl.substringAfter(',', "")
            return Base64.getDecoder().decode(encoded)
        }
        val key = image.sessionId.ifEmpty { sessionId } + "/" + image.attachmentId
        images[key]?.let { return it }
        val path = ApiClient.sessionPath(image.sessionId.ifEmpty { sessionId }) + "/attachments/" +
            java.net.URLEncoder.encode(image.attachmentId, "UTF-8") + "/raw"
        return api.download(path).also { images[key] = it }
    }

    companion object {
        const val MAX_ATTACHMENT_BYTES = 4 * 1024 * 1024
    }
}
