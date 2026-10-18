package com.hwj.easyagent.mobile.backend

import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject

/**
 * desktop-native/projection.go 的 Kotlin 移植：把服务端信封归约成可渲染的消息列表。
 * 字段语义沿用 Go 版：kind 为空且 user=false 表示助手正文；kind 为空且 text 为空表示等待占位。
 */
class ChatMessage(val uid: Long) {
    var user = false
    var text = ""
    var tool = ""
    var kind = ""
    var callId = ""
    var runId = ""
    var entryId = ""
    var args: JsonElement? = null
    var result = ""
    var details: JsonObject? = null
    var status = ""
    var durationMs = 0.0
    var expanded = false
    var expansionTouched = false

    val isWaiting: Boolean get() = !user && tool == "" && kind == "" && text == ""
}

class ChatSession(val id: String) {
    val messages = ArrayList<ChatMessage>()
    var run: RunInfo? = null
    var seq = 0L
    var confirmations: List<Confirmation> = emptyList()
    var queue = MessageQueue()
    var running = false
    var pending = false
    var sending = false
    var pendingRequest = ""
    var sentDraft = ""
    /** 服务端确认接收后递增，UI 据此清空与 [sentDraft] 一致的草稿。 */
    var draftClearCount = 0L
    var usage: JsonObject? = null
    private var nextUid = 1L

    val runId: String get() = run?.id ?: ""

    private fun add(configure: ChatMessage.() -> Unit): ChatMessage {
        val m = ChatMessage(nextUid++).apply(configure)
        messages.add(m)
        return m
    }

    fun history(records: List<StoredMessage>) {
        messages.clear()
        val tools = HashMap<String, ChatMessage>()
        for (m in records) {
            when (m.role) {
                "user" -> add { user = true; text = displayText(m.content); entryId = m.entryId }
                "assistant" -> {
                    if (m.thinking != "") add { tool = "思考过程"; text = m.thinking; kind = "thought" }
                    val body = displayText(m.content)
                    if (body != "") add { text = body; entryId = m.entryId }
                    for (call in m.toolCalls) {
                        val arguments = toolArguments(call.args)
                        tools[call.id] = add {
                            tool = call.name; callId = call.id; args = arguments; text = displayText(arguments)
                            kind = "tool"; entryId = m.entryId; status = "未完成"
                        }
                    }
                }
                "tool" -> tools[m.toolId]?.let { tool ->
                    tool.result = displayText(m.content)
                    tool.details = m.details
                    tool.durationMs = m.durationMs
                    tool.status = if (m.isError) "失败" else "完成"
                    if (m.details?.getString("approval") == "declined") tool.status = "已拒绝"
                }
                "compaction" -> add { tool = "上下文压缩"; text = m.compaction?.getString("summary") ?: ""; kind = "compaction" }
            }
        }
    }

    fun event(e: AgentEvent) {
        if (e.usage != null) usage = e.usage
        when (e.type) {
            "text_delta", "thinking_delta" -> {
                val k = if (e.type == "thinking_delta") "thought" else ""
                var m: ChatMessage? = null
                messages.lastOrNull()?.let { last ->
                    if (k == "thought" && last.kind == "" && last.text == "" && last.tool == "" && last.runId == runId) {
                        last.kind = "thought"; last.tool = "思考过程"
                    }
                    if (!last.user && last.kind == k && last.runId == runId && last.tool == "") m = last
                    if (k == "thought" && last.kind == k && last.runId == runId) m = last
                }
                val target = m ?: add { kind = k; runId = this@ChatSession.runId; if (k == "thought") tool = "思考过程" }
                target.text += e.text
            }
            "thinking_end" -> messages.lastOrNull { it.kind == "thought" && it.runId == runId }?.let { it.durationMs = e.durationMs }
            "context_usage" -> ensureWaiting()
            "tool_start" -> {
                removeWaiting()
                if (messages.any { it.callId == e.toolId && it.runId == runId }) return
                val arguments = toolArguments(e.args)
                add { tool = e.toolName; callId = e.toolId; runId = this@ChatSession.runId; kind = "tool"; status = "执行中"; args = arguments; text = displayText(arguments) }
            }
            "tool_update", "tool_end" -> {
                val m = messages.firstOrNull { it.callId == e.toolId && it.runId == runId } ?: return
                var result = e.partial
                if (e.type == "tool_end") {
                    result = e.result
                    m.status = "完成"
                    m.durationMs = e.durationMs
                    if (e.isError) {
                        m.status = "失败"
                        m.expanded = true
                    }
                    if (e.details?.getString("approval") == "declined") m.status = "已拒绝"
                }
                m.result = displayText(result)
                if (e.details != null) m.details = e.details
            }
            "compacted" -> add { tool = "上下文压缩"; text = e.summary; kind = "compaction" }
            "error", "compaction_failed" -> {
                removeWaiting()
                add { text = e.errorText; kind = "error" }
            }
            "done" -> {
                val fin = e.final
                val finalText = if (fin == null) "" else fin.text.ifEmpty { displayText(fin.content) }
                var has = false
                for (m in messages) {
                    if (m.runId == runId && m.kind == "" && !m.user) {
                        if (m.text == "") m.text = finalText
                        has = true
                    }
                }
                if (!has && finalText != "") add { text = finalText; runId = this@ChatSession.runId }
            }
        }
    }

    fun reduce(e: Envelope) {
        e.queue?.let { if (it.revision >= queue.revision) queue = it }
        when (e.type) {
            "snapshot" -> {
                if (!e.reset && e.seq < seq) return
                history(e.messages)
                run = e.run
                seq = e.seq
                confirmations = e.confirmations
                val r = e.run
                if (r != null && r.prompt != "") {
                    val found = messages.any { r.userEntry != "" && it.entryId == r.userEntry }
                    if (!found) add { user = true; text = r.displayPrompt.ifEmpty { r.prompt }; entryId = r.userEntry; runId = r.id }
                }
                for (raw in e.events) {
                    runCatching { wireJson.decodeFromJsonElement(AgentEvent.serializer(), raw) }.getOrNull()?.let(::event)
                }
            }
            "replay" -> {
                if (e.seq < seq) return
                if (run == null && e.run != null) run = e.run.copy(state = "running")
                for (raw in e.events) {
                    runCatching { wireJson.decodeFromJsonElement(Envelope.serializer(), raw) }.getOrNull()?.let(::reduce)
                }
                run = e.run
                seq = maxOf(seq, e.seq)
                confirmations = e.confirmations
            }
            "accepted" -> {
                if (e.seq > 0 && e.seq <= seq) return
                seq = maxOf(seq, e.seq)
                if (runId != e.runId) {
                    run = RunInfo(id = e.runId, requestId = e.requestId, state = e.state, prompt = e.prompt)
                    if (e.prompt != "") add { user = true; text = e.prompt; runId = e.runId }
                }
                ensureWaiting()
            }
            else -> {
                if (e.seq > 0 && e.seq <= seq) return
                if (e.runId != "" && runId != "" && e.runId != runId) return
                seq = maxOf(seq, e.seq)
                when (e.type) {
                    "event" -> e.event?.let(::event)
                    "confirmation" -> e.confirmation?.let { c ->
                        if (confirmations.none { it.id == c.id }) confirmations = confirmations + c
                    }
                    "confirmed" -> confirmations = confirmations.filter { it.id != e.confirmationId }
                    "status" -> {
                        if (e.run != null) run = e.run
                        run?.let { current ->
                            val state = e.state.ifEmpty { if (e.streaming) "running" else "completed" }
                            run = current.copy(state = state)
                        }
                    }
                    "error" -> add { kind = "error"; text = e.message + e.error }
                }
            }
        }
        if (e.type == "snapshot" && run?.active == true && e.events.isEmpty()) ensureWaiting()
        running = run?.active == true
        pending = confirmations.isNotEmpty()
        val current = run
        if (current != null && !current.active) {
            removeWaiting()
            confirmations = emptyList()
            pending = false
        }
    }

    fun statusText(): String = when {
        sending -> "正在提交"
        pending -> "等待审批"
        running -> "生成中"
        run?.state == "failed" -> "任务失败"
        run?.state == "interrupted" -> "任务已中断"
        else -> "就绪"
    }

    private fun ensureWaiting() {
        val current = run
        if (current == null || !current.active) return
        messages.lastOrNull()?.let { m ->
            if (!m.user && m.runId == runId && (m.kind == "" || m.kind == "thought")) return
        }
        add { runId = this@ChatSession.runId }
    }

    private fun removeWaiting() {
        messages.removeAll { it.isWaiting && it.runId == runId }
    }
}
