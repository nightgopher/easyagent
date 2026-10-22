package com.hwj.easyagent.mobile

import android.content.pm.ActivityInfo
import android.graphics.Bitmap
import android.graphics.Color
import android.os.Build
import android.os.ParcelFileDescriptor
import android.content.res.Configuration
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.text.AnnotatedString
import androidx.lifecycle.Lifecycle
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.espresso.Espresso.closeSoftKeyboard
import androidx.test.platform.app.InstrumentationRegistry
import com.hwj.easyagent.mobile.backend.SettingsStore
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.json.JSONArray
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.*
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** Runs the shipped activity against authenticated REST + WebSocket fixtures. */
@RunWith(AndroidJUnit4::class)
class RuntimeUiTest {
    @get:Rule val ui = createEmptyComposeRule()
    private val instrumentation = InstrumentationRegistry.getInstrumentation()
    private val app get() = instrumentation.targetContext.applicationContext as EasyAgentApp
    private lateinit var activity: ActivityScenario<MainActivity>
    private val hosts = mutableListOf<RuntimeFixture>()

    @Before fun launch() {
        instrumentation.runOnMainSync {
            app.connections.state.value.hosts.map { it.id }.forEach(app.connections::remove)
        }
        activity = ActivityScenario.launch(MainActivity::class.java)
    }

    @After fun close() {
        instrumentation.uiAutomation.executeShellCommand("cmd uimode night no").close()
        activity.close()
        instrumentation.runOnMainSync {
            app.connections.state.value.hosts.map { it.id }.forEach(app.connections::remove)
        }
        hosts.forEach { it.close() }
    }

    private fun host(name: String) = RuntimeFixture(name).also { hosts += it }
    private fun waitText(text: String) {
        try {
            ui.waitUntil(15_000) { ui.onAllNodesWithText(text, substring = true).fetchSemanticsNodes().isNotEmpty() }
        } catch (error: ComposeTimeoutException) {
            screenshot("failure")
            var projected = ""
            instrumentation.runOnMainSync {
                projected = app.connections.client.value?.activeChat?.messages?.joinToString(" | ") { it.text }.orEmpty()
            }
            throw AssertionError("Missing $text\nProjected messages: $projected\n${ui.onRoot().printToString()}", error)
        }
    }
    private fun fill(label: String, value: String) {
        ui.onNodeWithText(label).performScrollTo().performTextReplacement(value)
    }
    private fun connect(host: RuntimeFixture, token: String = "qa-token") {
        fill("连接名称（可选）", host.name)
        fill("服务地址", host.url)
        fill("服务令牌", token)
        ui.onNodeWithText("保存并连接").performScrollTo().performClick()
        waitText(host.title)
    }
    private fun openChat(host: RuntimeFixture) {
        ui.onNodeWithText(host.title).performClick()
        waitText("已保存的历史")
        waitText("已连接")
    }
    private fun prompt(text: String) {
        ui.onNode(hasSetTextAction()).performTextReplacement(text)
        closeSoftKeyboard()
        ui.waitUntil(10_000) {
            ui.onAllNodes(hasContentDescription("发送") and isEnabled()).fetchSemanticsNodes().isNotEmpty()
        }
        ui.onNodeWithContentDescription("发送").performClick()
    }
    private fun screenshot(name: String) {
        ui.waitForIdle()
        if (Build.VERSION.SDK_INT >= 31) {
            // Surface rotation can briefly make UiAutomation return no frame.
            var bitmap: Bitmap? = null
            ui.waitUntil(10_000) {
                bitmap = instrumentation.uiAutomation.takeScreenshot()
                bitmap != null
            }
            // AGP uninstalls both APKs after connected tests. Shell-owned output
            // survives that cleanup and is collected as a CI artifact.
            ParcelFileDescriptor.AutoCloseInputStream(instrumentation.uiAutomation.executeShellCommand("mkdir -p /data/local/tmp/easyagent-ui")).use { it.readBytes() }
            val pipes = instrumentation.uiAutomation.executeShellCommandRw("dd of=/data/local/tmp/easyagent-ui/$name.png")
            ParcelFileDescriptor.AutoCloseOutputStream(pipes[1]).use { stream ->
                check(requireNotNull(bitmap).compress(Bitmap.CompressFormat.PNG, 100, stream))
            }
            ParcelFileDescriptor.AutoCloseInputStream(pipes[0]).use { it.readBytes() }
            bitmap?.recycle()
        }
    }

    @Test fun connectionRejects401AndPersistsEncryptedCredentials() {
        val host = host("验收主机")
        fill("服务地址", host.url)
        fill("服务令牌", "incorrect")
        ui.onNodeWithText("测试连接").performScrollTo().performClick()
        waitText("401")
        screenshot("connection-401")
        fill("服务令牌", "qa-token")
        ui.onNodeWithText("测试连接").performScrollTo().performClick()
        waitText("连接成功")
        connect(host)
        activity.recreate()
        waitText(host.title)
        val saved = SettingsStore(instrumentation.targetContext).load()
        assertEquals("qa-token", saved.hosts.single().token)
        assertEquals(host.url.trimEnd('/'), saved.hosts.single().url)
        val prefs = instrumentation.targetContext.getSharedPreferences("easyagent-host", 0)
        assertFalse(prefs.all.values.any { it.toString() == "qa-token" })
        screenshot("connected-sessions")
    }

    @Test fun multipleHostsKeepIndependentDrafts() {
        val first = host("主机 A")
        val second = host("主机 B")
        connect(first)
        openChat(first)
        ui.onNode(hasSetTextAction()).performTextReplacement("A 的未发送草稿")
        ui.onNodeWithContentDescription("返回").performClick()
        ui.onNodeWithContentDescription("切换运行主机").performClick()
        ui.onNodeWithText("管理运行主机").performClick()
        ui.onNodeWithContentDescription("添加主机").performClick()
        connect(second)
        openChat(second)
        ui.onNode(hasSetTextAction()).assert(SemanticsMatcher.expectValue(SemanticsProperties.EditableText, AnnotatedString("")))
        ui.onNodeWithContentDescription("返回").performClick()
        ui.onNodeWithContentDescription("切换运行主机").performClick()
        ui.onNodeWithText("主机 A", substring = true).performClick()
        waitText(first.title)
        openChat(first)
        ui.onNode(hasSetTextAction()).assertTextEquals("A 的未发送草稿")
        screenshot("independent-drafts")
    }

    @Test fun streamingApprovalQueueStopAndReconnect() {
        val host = host("任务主机")
        connect(host)
        openChat(host)
        prompt("执行验收任务")
        val sent = host.prompts.poll(10, TimeUnit.SECONDS)
        assertNotNull("prompt must arrive over the real WebSocket", sent)
        host.accept(sent!!)
        waitText("生成中…")
        host.emit("event", "event" to JSONObject("""{"type":"text_delta","text_delta":"流式验收回答"}"""))
        waitText("流式验收回答")
        host.emit("event", "event" to JSONObject("""{"type":"text_delta","text_delta":"继续生成"}"""))
        waitText("流式验收回答继续生成")
        host.emit("confirmation", "confirmation" to JSONObject("""{"confirmation_id":"c1","tool_name":"write","description":"写入验收文件","args":{"path":"qa.txt"}}"""))
        waitText("写入验收文件")
        ui.onNodeWithText("批准").performScrollTo()
        screenshot("tool-approval")
        ui.onNodeWithText("批准").performClick()
        ui.waitUntil(10_000) { host.calls.any { it.path?.endsWith("/run/confirm") == true } }
        val approved = JSONObject(host.calls.first { it.path?.endsWith("/run/confirm") == true }.body.readUtf8())
        assertTrue(approved.getBoolean("approved"))
        prompt("下一项任务")
        waitText("排队中 1 条消息")
        ui.onNodeWithContentDescription("停止当前任务").performClick()
        ui.waitUntil(10_000) { host.calls.any { it.path?.endsWith("/run/cancel") == true } }
        val count = host.socketCount.get()
        activity.moveToState(Lifecycle.State.CREATED)
        host.socket?.close(1001, "test reconnect")
        activity.moveToState(Lifecycle.State.RESUMED)
        ui.waitUntil(15_000) { host.socketCount.get() > count }
        waitText("已保存的历史")
        activity.onActivity { it.requestedOrientation = ActivityInfo.SCREEN_ORIENTATION_LANDSCAPE }
        ui.waitForIdle()
        screenshot("landscape-reconnected")
        instrumentation.uiAutomation.executeShellCommand("cmd uimode night yes").close()
        ui.waitUntil(5_000) { instrumentation.targetContext.resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK == Configuration.UI_MODE_NIGHT_YES }
        ui.waitUntil(5_000) {
            val bitmap = instrumentation.uiAutomation.takeScreenshot() ?: return@waitUntil false
            val dark = Color.luminance(bitmap.getPixel(bitmap.width / 2, bitmap.height / 12)) < 0.3f
            bitmap.recycle()
            dark
        }
        screenshot("dark-landscape")
    }

    @Test fun approvalCanBeRejected() {
        val host = host("审批主机")
        connect(host)
        openChat(host)
        prompt("需要拒绝的操作")
        host.accept(requireNotNull(host.prompts.poll(10, TimeUnit.SECONDS)))
        host.emit("confirmation", "confirmation" to JSONObject("""{"confirmation_id":"c1","tool_name":"write","description":"拒绝验收操作"}"""))
        waitText("拒绝验收操作")
        ui.onNodeWithText("拒绝").performScrollTo().performClick()
        ui.waitUntil(10_000) { host.calls.any { it.path?.endsWith("/run/confirm") == true } }
        assertFalse(JSONObject(host.calls.first { it.path?.endsWith("/run/confirm") == true }.body.readUtf8()).getBoolean("approved"))
    }

    @Test fun thinkingAndToolCardsRefreshInPlace() {
        val host = host("实时卡片主机")
        connect(host)
        openChat(host)
        prompt("逐段验证实时卡片")
        host.accept(requireNotNull(host.prompts.poll(10, TimeUnit.SECONDS)))
        waitText("生成中…")
        host.emit("event", "event" to JSONObject("""{"type":"thinking_delta","text_delta":"第一段思考"}"""))
        waitText("第一段思考")
        host.emit("event", "event" to JSONObject("""{"type":"thinking_delta","text_delta":"第二段思考"}"""))
        waitText("第一段思考第二段思考")
        ui.onNodeWithText("思考过程").performClick()
        ui.waitUntil(5_000) { ui.onAllNodesWithText("第一段思考第二段思考").fetchSemanticsNodes().isEmpty() }
        ui.onNodeWithText("思考过程").performClick()
        waitText("第一段思考第二段思考")
        host.emit("event", "event" to JSONObject("""{"type":"tool_start","tool_call_id":"t1","tool_name":"bash","tool_args":{"command":"printf QA_TOOL"}}"""))
        waitText("printf QA_TOOL")
        waitText("执行中")
        ui.onNodeWithText("bash").performScrollTo().performClick()
        waitText("调用参数")
        host.emit("event", "event" to JSONObject("""{"type":"tool_update","tool_call_id":"t1","partial_result":"第一段输出"}"""))
        waitText("第一段输出")
        host.emit("event", "event" to JSONObject("""{"type":"tool_end","tool_call_id":"t1","tool_result":"完整输出","duration_ms":1500}"""))
        waitText("完整输出")
        waitText("完成")
        screenshot("live-message-cards")
        ui.onNodeWithText("bash").performClick()
        ui.waitUntil(5_000) { ui.onAllNodesWithText("完整输出").fetchSemanticsNodes().isEmpty() }
    }
}

private class RuntimeFixture(val name: String) : AutoCloseable {
    private val server = MockWebServer()
    val title = "$name 会话"
    val url: String get() = server.url("/").toString()
    val calls = ConcurrentLinkedQueue<RecordedRequest>()
    val prompts = LinkedBlockingQueue<JSONObject>()
    val socketCount = AtomicInteger()
    @Volatile var socket: WebSocket? = null
    private val sequence = AtomicInteger()
    @Volatile private var running = false
    private var runPrompt = ""
    private var requestId = ""
    private var events = JSONArray()
    private var confirmation: JSONObject? = null

    init {
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                calls += request
                val path = request.requestUrl!!.encodedPath
                if (path != "/health" && request.getHeader("Authorization") != "Bearer qa-token") {
                    return json("""{"error":"invalid token"}""").setResponseCode(401)
                }
                return when {
                    path == "/health" -> json("""{"status":"ok","version":"qa"}""")
                    path == "/ws" -> MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                        override fun onOpen(webSocket: WebSocket, response: Response) {
                            socket = webSocket
                            socketCount.incrementAndGet()
                        }
                        override fun onMessage(webSocket: WebSocket, raw: String) {
                            val value = JSONObject(raw)
                            when (value.optString("type")) {
                                "subscribe" -> webSocket.send(snapshot().toString())
                                "prompt" -> prompts.offer(value)
                            }
                        }
                    })
                    path == "/sessions" && request.method == "GET" -> json(JSONArray().put(JSONObject().put("id", "qa-session").put("title", title)).toString())
                    path.endsWith("/run/confirm") -> {
                        emit("confirmed", "confirmation_id" to "c1")
                        json("{}")
                    }
                    path.endsWith("/run/cancel") -> {
                        emit("status", "state" to "cancelled", "streaming" to false)
                        json("{}")
                    }
                    path.endsWith("/queue") -> json("""{"revision":1,"items":[{"id":"q1","prompt":"下一项任务"}]}""")
                    path.endsWith("/run") -> json(snapshot().toString())
                    else -> json("{}").setResponseCode(404)
                }
            }
        }
        server.start()
    }

    @Synchronized fun accept(prompt: JSONObject) {
        running = true
        runPrompt = prompt.getString("prompt")
        requestId = prompt.getString("request_id")
        events = JSONArray()
        emit("accepted", "request_id" to prompt.getString("request_id"), "prompt" to prompt.getString("prompt"), "state" to "running")
    }

    @Synchronized fun emit(type: String, vararg fields: Pair<String, Any>) {
        val value = JSONObject().put("type", type).put("session_id", "qa-session")
            .put("run_id", "qa-run").put("seq", sequence.incrementAndGet())
        for ((key, field) in fields) value.put(key, field)
        if (type == "event") events.put(value.getJSONObject("event"))
        if (type == "status") running = value.optString("state") == "running"
        if (type == "confirmation") confirmation = value.getJSONObject("confirmation")
        if (type == "confirmed" || type == "status") confirmation = null
        check(socket?.send(value.toString()) == true)
    }

    @Synchronized private fun snapshot(): JSONObject {
        val messages = JSONArray().put(JSONObject().put("role", "assistant").put("content", "已保存的历史"))
        val value = JSONObject().put("type", "snapshot").put("session_id", "qa-session")
            .put("seq", sequence.get()).put("messages", messages)
            .put("events", JSONArray(events.toString()))
            .put("confirmations", JSONArray().also { list -> confirmation?.let { list.put(it) } })
        if (runPrompt.isNotEmpty()) value.put("run", JSONObject().put("id", "qa-run")
            .put("state", if (running) "running" else "cancelled").put("prompt", runPrompt).put("request_id", requestId))
        return value
    }

    private fun json(body: String) = MockResponse().setHeader("Content-Type", "application/json").setBody(body)
    override fun close() { socket?.close(1000, "test complete"); server.shutdown() }
}
