package com.hwj.easyagent.mobile.backend

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.ExperimentalSerializationApi
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import java.time.Instant

// 与 desktop-native/projection.go 中的结构一一对应，字段名以服务端 JSON 为准。

val wireJson: Json = Json {
    ignoreUnknownKeys = true
    isLenient = true
    coerceInputValues = true
    explicitNulls = false
}

@Serializable
data class SessionSummary(
    val id: String = "",
    val title: String = "",
    @SerialName("last_active") val lastActive: Long = 0,
    @SerialName("created_at") val createdAt: Long = 0,
    val workspace: String = "",
    val application: String = "",
    val archived: Boolean = false,
    val pinned: Boolean = false,
    /** "running" or "waiting_confirmation" while a run is active, else empty. */
    @SerialName("run_state") val runState: String = "",
    @SerialName("pending_confirmations") val pendingConfirmations: Int = 0,
) {
    val running: Boolean get() = runState == "running" || runState == "waiting_confirmation"

    val updatedAt: String
        get() = (lastActive.takeIf { it > 0 } ?: createdAt.takeIf { it > 0 })
            ?.let { Instant.ofEpochSecond(it).toString() }.orEmpty()

    val displayTitle: String
        get() = title.ifBlank { "会话 " + id.takeLast(6) }
}

@Serializable
data class RunInfo(
    @SerialName("run_id") val id: String = "",
    @SerialName("request_id") val requestId: String = "",
    val state: String = "",
    val prompt: String = "",
    @SerialName("display_prompt") val displayPrompt: String = "",
    @SerialName("user_entry_id") val userEntry: String = "",
    @SerialName("assistant_entry_id") val assistantEntry: String = "",
    val error: String = "",
    @SerialName("can_continue") val canContinue: Boolean = false,
) {
    val active: Boolean get() = state == "running" || state == "waiting_confirmation"
}

@Serializable
data class Confirmation(
    @SerialName("confirmation_id") val id: String = "",
    @SerialName("tool_call_id") val toolCallId: String = "",
    @SerialName("tool_name") val toolName: String = "",
    val description: String = "",
    val args: JsonElement? = null,
) {
    /** ask_user_question 的确认需要回答表单而不是批准/拒绝。 */
    val isQuestion: Boolean get() = toolName == "ask_user_question"
}

@Serializable
data class UserOption(
    val label: String = "",
    val description: String = "",
    val preview: String = "",
)

@Serializable
data class UserQuestion(
    val question: String = "",
    val header: String = "",
    val options: List<UserOption> = emptyList(),
    @SerialName("multiSelect") val multiSelect: Boolean = false,
)

/** GET /sessions/{id}/info 的子集：权限模式、当前模型与上下文占用。 */
@Serializable
data class SessionInfo(
    @SerialName("access_mode") val accessMode: String = "",
    val provider: String = "",
    val model: String = "",
    @SerialName("context_usage") val contextUsage: ContextUsage? = null,
)

@Serializable
data class ContextUsage(
    @SerialName("estimated_tokens") val estimatedTokens: Long = 0,
    @SerialName("context_window") val contextWindow: Long = 0,
    @SerialName("window_known") val windowKnown: Boolean = false,
    val model: String = "",
    val messages: Int = 0,
) {
    /** 已知窗口时返回占用百分比，未知窗口返回 -1。 */
    val percent: Int
        get() = if (windowKnown && contextWindow > 0) (estimatedTokens * 100 / contextWindow).toInt() else -1
}

@Serializable
data class ModelInfo(
    val id: String = "",
    val provider: String = "",
    val name: String = "",
    @SerialName("context_window") val contextWindow: Long = 0,
)

@Serializable
data class ModelsResponse(
    val models: List<ModelInfo> = emptyList(),
    val current: ModelInfo? = null,
    @SerialName("discovery_error") val discoveryError: String = "",
)

/** ask_user_question 的参数存在 confirmation.args.questions；解析失败返回 null，由 UI 退回普通审批卡。 */
fun questionsFromArgs(args: JsonElement?): List<UserQuestion>? {
    val raw = args.obj()["questions"] ?: return null
    return runCatching {
        wireJson.decodeFromJsonElement(ListSerializer(UserQuestion.serializer()), raw)
    }.getOrNull()?.takeIf { it.isNotEmpty() }
}

/** 一条问题的回答：selected 是选项 label，text 是"其他"自填内容。 */
fun answerJson(selected: List<String>, text: String): JsonObject = buildJsonObject {
    put("selected", JsonArray(selected.map { JsonPrimitive(it) }))
    if (text.isNotBlank()) put("text", text)
}

/** 一次问答的全部回答，按问题顺序。 */
fun answersJson(answers: List<JsonObject>): JsonArray = JsonArray(answers)

/** 单选问题是否已答：恰好一个选项，或只填了自填内容；多选至少一项（自填内容算一项）。 */
fun questionAnswered(question: UserQuestion, selected: Set<String>, text: String): Boolean =
    if (question.multiSelect) selected.isNotEmpty() || text.isNotBlank()
    else (selected.size == 1) != text.isNotBlank()

/** 组装 POST /run/confirm 的 answers 数组（按问题顺序）；有任何一题未答返回 null。 */
fun buildAnswers(questions: List<UserQuestion>, selections: List<Set<String>>, texts: List<String>): JsonArray? {
    if (questions.size != selections.size || selections.size != texts.size) return null
    val answers = ArrayList<JsonObject>(questions.size)
    for (i in questions.indices) {
        if (!questionAnswered(questions[i], selections[i], texts[i])) return null
        answers += answerJson(selections[i].toList(), texts[i])
    }
    return JsonArray(answers)
}

@Serializable
data class QueueItem(
    val id: String = "",
    val prompt: String = "",
)

@Serializable
data class MessageQueue(
    val items: List<QueueItem> = emptyList(),
    val paused: Boolean = false,
    val revision: Long = 0,
)

@Serializable
data class ToolCall(
    val id: String = "",
    val name: String = "",
    val args: JsonElement? = null,
)

@Serializable
data class StoredMessage(
    @SerialName("stop_reason") val stopReason: String = "",
    @SerialName("content_hash") val contentHash: String = "",
    @SerialName("duration_ms") val durationMs: Double = 0.0,
    val role: String = "",
    val content: JsonElement? = null,
    val thinking: String = "",
    @SerialName("entry_id") val entryId: String = "",
    @SerialName("tool_call_id") val toolId: String = "",
    @SerialName("is_error") val isError: Boolean = false,
    @SerialName("tool_calls") val toolCalls: List<ToolCall> = emptyList(),
    val compaction: JsonObject? = null,
    @SerialName("tool_details") val details: JsonObject? = null,
    val images: List<HistoryImage> = emptyList(),
)

/** An image in a user message: an attachment to fetch, or inline data. */
@Serializable
data class HistoryImage(
    @SerialName("attachmentId") val attachmentId: String = "",
    @SerialName("sessionId") val sessionId: String = "",
    @SerialName("data_url") val dataUrl: String = "",
)

/** An attachment uploaded for the next prompt (server inputAttachment). */
@Serializable
data class UploadedAttachment(
    val id: String = "",
    val name: String = "",
    @SerialName("mime_type") val mimeType: String = "",
    val size: Long = 0,
)

@Serializable
data class FinalMessage(
    val text: String = "",
    val content: JsonElement? = null,
)

@Serializable
data class AgentEvent(
    @SerialName("duration_ms") val durationMs: Double = 0.0,
    val type: String = "",
    @SerialName("text_delta") val text: String = "",
    @SerialName("tool_call_id") val toolId: String = "",
    @SerialName("tool_name") val toolName: String = "",
    @SerialName("tool_args") val args: JsonElement? = null,
    @SerialName("tool_result") val result: JsonElement? = null,
    @SerialName("partial_result") val partial: JsonElement? = null,
    @SerialName("tool_details") val details: JsonObject? = null,
    @SerialName("is_error") val isError: Boolean = false,
    @SerialName("error") val errorText: String = "",
    val summary: String = "",
    @SerialName("context_usage") val usage: JsonObject? = null,
    @SerialName("final_message") val final: FinalMessage? = null,
)

/** A session's watchdog loop (GET /sessions/{id}/loop). Times are RFC 3339. */
@Serializable
data class LoopStatus(
    val prompt: String = "",
    @SerialName("interval_seconds") val intervalSeconds: Long = 0,
    @SerialName("started_at") val startedAt: String = "",
    @SerialName("expires_at") val expiresAt: String = "",
    @SerialName("last_run_at") val lastRunAt: String = "",
    val pending: Boolean = false,
)

@Serializable
data class Envelope(
    val type: String = "",
    @SerialName("session_id") val sessionId: String = "",
    @SerialName("request_id") val requestId: String = "",
    @SerialName("run_id") val runId: String = "",
    val seq: Long = 0,
    val reset: Boolean = false,
    val state: String = "",
    val streaming: Boolean = false,
    val prompt: String = "",
    val message: String = "",
    val error: String = "",
    val run: RunInfo? = null,
    val messages: List<StoredMessage> = emptyList(),
    val events: List<JsonElement> = emptyList(),
    val event: AgentEvent? = null,
    val confirmation: Confirmation? = null,
    @SerialName("confirmation_id") val confirmationId: String = "",
    @SerialName("pending_confirmations") val confirmations: List<Confirmation> = emptyList(),
    val queue: MessageQueue? = null,
)

/** 对应 Go 的 displayText：内容可能是字符串、内容块数组或带 text/content 字段的对象。 */
fun displayText(v: JsonElement?): String {
    return when (v) {
        null, JsonNull -> ""
        is JsonPrimitive -> v.content
        is JsonArray -> buildString {
            for (part in v) {
                if (part is JsonObject) {
                    if (part["type"].str() == "image") append("[图片]") else append(displayText(part["text"]))
                } else {
                    append(displayText(part))
                }
            }
        }
        is JsonObject -> {
            for (key in listOf("UserFacing", "user_facing", "Content", "content", "text")) {
                val inner = v[key]
                if (inner != null && inner !is JsonNull) return displayText(inner)
            }
            pretty(v)
        }
    }
}

/** 工具参数在历史记录里是 JSON 字符串，流式事件里是对象；统一解成结构。 */
fun toolArguments(args: JsonElement?): JsonElement? {
    if (args is JsonPrimitive && args.isString) {
        return runCatching { wireJson.parseToJsonElement(args.content) }.getOrNull() ?: args
    }
    return args
}

fun pretty(v: JsonElement?): String {
    if (v == null || v is JsonNull) return ""
    return prettyJson.encodeToString(JsonElement.serializer(), v)
}

@OptIn(ExperimentalSerializationApi::class)
private val prettyJson = Json { prettyPrint = true; prettyPrintIndent = "  " }

fun JsonElement?.str(): String = (this as? JsonPrimitive)?.takeIf { it.isString }?.content ?: ""

fun JsonElement?.obj(): JsonObject = this as? JsonObject ?: JsonObject(emptyMap())

fun JsonElement?.number(): Double = (this as? JsonPrimitive)?.content?.toDoubleOrNull() ?: 0.0

fun JsonObject.getString(key: String): String = this[key].str()
