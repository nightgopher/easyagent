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
import kotlinx.serialization.json.JsonElement
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
class AgentClient(val config: HostConfig, private val drafts: DraftStore = NoDrafts) {
    val api = ApiClient(config.url, requireNotNull(config.token))
    val link = MutableStateFlow(LinkStatus.CONNECTING)
    val sessions = MutableStateFlow<List<SessionSummary>>(emptyList())
    val taskStates = MutableStateFlow<Map<String, String>>(emptyMap())
    val unread = MutableStateFlow<Set<String>>(emptySet())
    private var reconcileOnRefresh = true
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

    private val credentialScope = java.security.MessageDigest.getInstance("SHA-256")
        .digest((config.url + "\n" + config.token.orEmpty()).toByteArray()).joinToString("") { "%02x".format(it) }.take(24)
    val storageIdentity: String get() = config.id + ":" + credentialScope
    fun chat(id: String): ChatSession = chats.getOrPut(id) {
        ChatSession(id).also { session ->
            drafts.load(storageIdentity, id).takeIf { it.isNotBlank() }?.let { saved ->
                runCatching {
                    val data = wireJson.parseToJsonElement(saved).obj()
                    session.draft = data.getString("text")
                    session.references = (data["references"] as? JsonArray).orEmpty().map { it.obj() }
                    session.sentReferences = (data["sentReferences"] as? JsonArray).orEmpty().map { it.obj() }
                    session.pendingRequest = data.getString("request")
                    session.sentDraft = data.getString("sent")
                    session.attachments = wireJson.decodeFromJsonElement(ListSerializer(UploadedAttachment.serializer()), data["attachments"] ?: JsonArray(emptyList())).map { PendingAttachment(it, null) }
                    session.sentAttachments = (data["sentAttachments"] as? JsonArray).orEmpty().map { it.str() }.toSet()
                }
            }
        }
    }
    private fun saveDrafts() {
        chats.values.forEach { s ->
            val data = buildJsonObject {
                put("references", JsonArray(s.references)); put("sentReferences", JsonArray(s.sentReferences))
                put("text", s.draft); put("request", s.pendingRequest); put("sent", s.sentDraft)
                put("attachments", wireJson.encodeToJsonElement(ListSerializer(UploadedAttachment.serializer()), s.attachments.map { it.attachment }))
                put("sentAttachments", JsonArray(s.sentAttachments.map { JsonPrimitive(it) }))
            }.toString()
            if (s.draft.isEmpty() && s.pendingRequest.isEmpty() && s.sentDraft.isEmpty() && s.attachments.isEmpty() && s.sentAttachments.isEmpty() && s.references.isEmpty() && s.sentReferences.isEmpty()) {
                drafts.clear(storageIdentity, s.id)
            } else if (drafts.load(storageIdentity, s.id) != data) drafts.save(storageIdentity, s.id, data)
        }
    }

    fun start() {
        connect()
        refreshSessions()
        scope.launch {
            while (isActive) {
                // Poll faster while a session works so list badges stay current.
                delay(if (sessions.value.any { it.running }) 10_000 else 60_000)
                if (link.value == LinkStatus.CONNECTED) refreshSessions()
            }
        }
    }

    /** 回到前台：立即重连并刷新会话列表。 */
    fun resume() {
        if (closed) return
        reconcileOnRefresh = true
        refreshSessions()
        chats.values.toList().forEach(::reload)
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
        saveDrafts()
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
                chats.values.forEach(::subscribe)
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
        val wasRunning = s.running
        s.reduce(e)
        s.run?.let { run ->
            taskStates.value = taskStates.value + (s.id to run.state)
            if (run.state in setOf("completed", "failed", "interrupted", "cancelled")) {
                val viewing = com.hwj.easyagent.mobile.AppVisibility.foreground && com.hwj.easyagent.mobile.AppVisibility.openChat == "${config.id}/${s.id}"
                if (viewing) { drafts.save(storageIdentity, "read:" + s.id, run.id); unread.value = unread.value - s.id }
                else if (drafts.load(storageIdentity, "read:" + s.id) != run.id) unread.value = unread.value + s.id
            }
        }
        onAlert?.let { notify -> alertsFor(before, s).forEach { notify(this, it) } }
        if (e.type == "status" && !s.running) refreshSessions()
        if (wasRunning && !s.running) loadSessionInfo(s)
        touch()
    }

    /** Session title for notifications, falling back to a short ID. */
    fun titleOf(sessionId: String): String =
        sessions.value.firstOrNull { it.id == sessionId }?.title?.takeIf { it.isNotBlank() } ?: "会话 " + sessionId.take(8)

    fun open(id: String) {
        activeId.value = id
        val s = chat(id)
        scope.launch {
            try { val info = api.get(ApiClient.sessionPath(id) + "/info").obj(); s.accessMode = info.getString("access_mode"); s.workspace = info.getString("workspace"); touch() }
            catch (e: ApiException) { error.value = e.message }
        }
        touch()
        reload(s)
        loadSessionInfo(s)
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
            put("files", JsonArray(s.references))
        }
        val attachments = s.attachments.map { it.attachment.id }.toSet()
        if (s.pendingRequest == "" || s.sentDraft != text || s.sentAttachments != attachments || s.sentReferences != s.references) s.pendingRequest = requestId()
        s.sentReferences = s.references
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

    fun confirm(s: ChatSession, confirmationId: String, approved: Boolean, answers: JsonElement? = null) {
        val body = buildJsonObject {
            put("run_id", s.runId)
            put("confirmation_id", confirmationId)
            put("approved", approved)
            if (answers != null) put("answers", answers)
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

    /** Models available on the host (GET /models), with the session-agnostic current entry. */
    suspend fun models(): ModelsResponse =
        wireJson.decodeFromJsonElement(ModelsResponse.serializer(), api.get("/models"))

    fun refreshSessions() {
        if (sessionsLoading.value) return
        sessionsLoading.value = true
        scope.launch {
            try {
                val raw = api.get("/sessions")
                val list = wireJson.decodeFromJsonElement(ListSerializer(SessionSummary.serializer()), raw)
                val finished = sessions.value.filter { it.running && list.none { next -> next.id == it.id && next.running } }.map { it.id }
                val reconcile = if (reconcileOnRefresh) list.filter { !it.archived }.sortedByDescending { it.lastActive }.take(12).map { it.id } else emptyList()
                reconcileOnRefresh = false
                (finished + reconcile).distinct().forEach { reload(chat(it)) }
                sessions.value = list.filter { it.id.isNotEmpty() }.sortedByDescending { it.pinned }
            } catch (e: ApiException) {
                error.value = e.message
            } finally {
                sessionsLoading.value = false
            }
        }
    }

    fun createSession(cwd: String = "", model: String = "", onCreated: (String) -> Unit) {
        scope.launch {
            try {
                val raw = api.post("/sessions", buildJsonObject { put("cwd", cwd); put("model", model) })
                val id = raw.obj().getString("id")
                if (id.isEmpty()) throw ApiException("服务未返回会话 ID")
                // Only newly created mobile sessions opt into full mode.
                try {
                    val mode = api.post(ApiClient.sessionPath(id) + "/permissions", buildJsonObject { put("mode", "full") })
                    chat(id).accessMode = mode.obj().getString("access_mode")
                } catch (e: ApiException) { error.value = "会话已创建，但全权限设置失败：" + e.message }
                refreshSessions()
                onCreated(id)
            } catch (e: ApiException) {
                error.value = e.message
            }
        }
    }

    fun continueRun(s: ChatSession) {
        call(ApiClient.sessionPath(s.id) + "/run/continue", buildJsonObject { put("run_id", s.runId); put("request_id", requestId()) }) { reload(s) }
    }

    fun deleteSession(id: String) {
        scope.launch {
            try {
                api.delete(ApiClient.sessionPath(id))
                chats.remove(id)
                drafts.clear(storageIdentity, id)
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

    /** Switches the session's approval mode ("ask" or "full"); the host refuses while a run is active. */
    fun setAccessMode(s: ChatSession, mode: String) {
        call(ApiClient.sessionPath(s.id) + "/permissions", buildJsonObject { put("mode", mode) }) {
            s.accessMode = mode
        }
    }

    /** Switches the session's model; info is reloaded afterwards. */
    fun switchModel(s: ChatSession, model: ModelInfo) {
        call(ApiClient.sessionPath(s.id) + "/model", buildJsonObject {
            put("model", model.id)
            put("provider", model.provider)
        }) { loadSessionInfo(s) }
    }

    /** Compacts the conversation context; the host summarizes history, which can take a while. */
    fun compact(s: ChatSession) {
        scope.launch {
            try {
                api.post(ApiClient.sessionPath(s.id) + "/compact")
                loadSessionInfo(s)
            } catch (e: ApiException) {
                error.value = e.message
            }
        }
    }

    /** Refreshes access mode, model and context usage from GET /sessions/{id}/info. */
    fun loadSessionInfo(s: ChatSession) {
        scope.launch {
            try {
                val raw = api.get(ApiClient.sessionPath(s.id) + "/info")
                val info = wireJson.decodeFromJsonElement(SessionInfo.serializer(), raw)
                s.accessMode = info.accessMode
                s.model = info.model
                info.contextUsage?.let { s.contextUsage = it }
            } catch (e: ApiException) {
                // 信息刷新失败不打扰用户：权限与用量维持上次值。
            }
            touch()
        }
    }

    /** Queue mutation (edit/remove/pause/resume/send_now) through POST /queue. */
    fun queueOp(s: ChatSession, action: String, id: String = "", prompt: String = "") {
        scope.launch {
            try {
                val body = buildJsonObject {
                    put("action", action)
                    if (id.isNotEmpty()) put("id", id)
                    if (prompt.isNotEmpty()) put("prompt", prompt)
                }
                val raw = api.post(ApiClient.sessionPath(s.id) + "/queue", body)
                runCatching { wireJson.decodeFromJsonElement(MessageQueue.serializer(), raw) }.getOrNull()?.let {
                    if (it.revision >= s.queue.revision) s.queue = it
                }
            } catch (e: ApiException) {
                error.value = e.message
            }
            touch()
        }
    }

    /** Persists the composer draft for this host+session; called on every edit. */
    fun saveDraft(s: ChatSession, text: String) {
        s.draft = text
        saveDrafts()
    }

    /** Authenticated URL for a message image, loaded by the shared image loader. */
    fun attachmentUrl(sessionId: String, image: HistoryImage): String =
        config.url + ApiClient.sessionPath(image.sessionId.ifEmpty { sessionId }) + "/attachments/" +
            java.net.URLEncoder.encode(image.attachmentId, "UTF-8") + "/raw"

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
        s.references = s.references.filterNot { it in s.sentReferences }
        s.sentDraft = ""
        s.sentReferences = emptyList()
        s.sentAttachments = emptySet()
        saveDraft(s, s.draft)
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

    companion object {
        const val MAX_ATTACHMENT_BYTES = 4 * 1024 * 1024
    }
}
