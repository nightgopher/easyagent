package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.DraftStore
import com.hwj.easyagent.mobile.backend.HostConfig
import com.hwj.easyagent.mobile.backend.LinkStatus
import com.hwj.easyagent.mobile.backend.MessageQueue
import com.hwj.easyagent.mobile.backend.NoDrafts
import com.hwj.easyagent.mobile.backend.QueueItem
import com.hwj.easyagent.mobile.backend.RunInfo
import com.hwj.easyagent.mobile.backend.answerJson
import com.hwj.easyagent.mobile.backend.answersJson
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.setMain
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

@OptIn(ExperimentalCoroutinesApi::class)
class AgentClientTest {
    private class MemoryDrafts : DraftStore {
        val map = HashMap<String, String>()
        override fun load(hostId: String, sessionId: String): String = map["$hostId/$sessionId"].orEmpty()
        override fun save(hostId: String, sessionId: String, text: String) { map["$hostId/$sessionId"] = text }
        override fun clear(hostId: String, sessionId: String) { map.remove("$hostId/$sessionId") }
    }

    private fun withClient(drafts: DraftStore = NoDrafts, block: suspend (MockWebServer, AgentClient) -> Unit) = runBlocking {
        val main = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val server = MockWebServer()
        server.start()
        Dispatchers.setMain(main)
        val client = AgentClient(HostConfig(server.url("/").toString().trimEnd('/'), ""), drafts)
        try {
            withContext(main) { block(server, client) }
        } finally {
            withContext(main) { client.shutdown(); client.awaitShutdown() }
            Dispatchers.resetMain()
            main.close()
            server.shutdown()
        }
    }

    @Test
    fun `queued send blocks duplicate taps and preserves newer queue revisions`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("""{"items":[],"revision":3}""").setBodyDelay(200, TimeUnit.MILLISECONDS))
        client.link.value = LinkStatus.CONNECTED
        val session = client.chat("s1")
        session.running = true
        session.draft = "next prompt"
        session.queue = MessageQueue(items = listOf(QueueItem("newer", "queued")), revision = 4)
        client.sendPrompt(session, "next prompt")
        assertTrue("queue submission must disable sending until acknowledged", session.sending)
        client.sendPrompt(session, "next prompt")
        val request = withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS) }
        assertEquals("/sessions/s1/queue", request?.path)
        withTimeout(5_000) { while (session.sending) delay(10) }
        assertEquals(1, server.requestCount)
        assertEquals(4, session.queue.revision)
        assertEquals("newer", session.queue.items.single().id)
        assertEquals("", session.pendingRequest)
        assertEquals("", session.draft)
    }

    @Test
    fun `failed queue submission preserves retry identity and draft`() = withClient { server, client ->
        server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"queue full"}"""))
        client.link.value = LinkStatus.CONNECTED
        val session = client.chat("s1")
        session.running = true
        session.draft = "keep this draft"
        client.sendPrompt(session, "keep this draft")
        val requestId = session.pendingRequest
        withTimeout(5_000) { while (client.error.value == null) delay(10) }
        assertFalse(session.sending)
        assertTrue(requestId.isNotEmpty())
        assertEquals(requestId, session.pendingRequest)
        assertEquals("keep this draft", session.sentDraft)
        assertEquals("keep this draft", session.draft)
    }

    @Test
    fun `confirm submits question answers with approval`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("{}"))
        client.link.value = LinkStatus.CONNECTED
        val session = client.chat("s1")
        session.run = RunInfo(id = "r1", state = "waiting_confirmation")
        client.confirm(session, "c1", true, answersJson(listOf(answerJson(listOf("方案 A"), ""))))
        val request = withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS) }
        assertEquals("/sessions/s1/run/confirm", request?.path)
        val body = request!!.body.readUtf8()
        assertTrue(body.contains("\"confirmation_id\":\"c1\""))
        assertTrue(body.contains("\"answers\":[{\"selected\":[\"方案 A\"]}]"))
    }

    @Test
    fun `continue posts the failed run id`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("{}"))
        client.link.value = LinkStatus.CONNECTED
        val session = client.chat("s1")
        session.run = RunInfo(id = "r9", state = "failed", canContinue = true)
        client.continueRun(session)
        val request = withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS) }
        assertEquals("/sessions/s1/run/continue", request?.path)
        assertTrue(request!!.body.readUtf8().contains("\"run_id\":\"r9\""))
    }

    @Test
    fun `access mode switch posts mode and updates session`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("""{"access_mode":"full"}"""))
        client.link.value = LinkStatus.CONNECTED
        val session = client.chat("s1")
        client.setAccessMode(session, "full")
        withTimeout(5_000) { while (session.accessMode != "full") delay(10) }
        val request = withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS) }
        assertEquals("/sessions/s1/permissions", request?.path)
        assertTrue(request!!.body.readUtf8().contains("\"mode\":\"full\""))
    }

    @Test
    fun `models parse the host catalog`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("""{"models":[{"id":"m1","provider":"openai","name":"Model One","context_window":128000}],"current":{"id":"m1","provider":"openai","name":"Model One"}}"""))
        val response = client.models()
        assertEquals(1, response.models.size)
        assertEquals("m1", response.models.single().id)
        assertEquals("Model One", response.models.single().name)
        assertEquals(128000, response.models.single().contextWindow)
        assertEquals("m1", response.current?.id)
    }

    @Test
    fun `queue operations post actions and adopt the returned queue`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("""{"items":[{"id":"q1","prompt":"edited text"}],"paused":false,"revision":7}"""))
        client.link.value = LinkStatus.CONNECTED
        val session = client.chat("s1")
        session.queue = MessageQueue(items = listOf(QueueItem("q1", "old")), revision = 6)
        client.queueOp(session, "edit", "q1", "edited text")
        withTimeout(5_000) { while (session.queue.revision < 7) delay(10) }
        val request = withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS) }
        assertEquals("/sessions/s1/queue", request?.path)
        val body = request!!.body.readUtf8()
        assertTrue(body.contains("\"action\":\"edit\""))
        assertTrue(body.contains("\"id\":\"q1\""))
        assertTrue(body.contains("\"prompt\":\"edited text\""))
        assertEquals("edited text", session.queue.items.single().prompt)
    }

    @Test
    fun `created sessions default to full access`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("""{"id":"new1"}"""))
        server.enqueue(MockResponse().setBody("""{"access_mode":"full"}"""))
        server.enqueue(MockResponse().setBody("[]"))
        client.link.value = LinkStatus.CONNECTED
        var created = ""
        client.createSession { created = it }
        withTimeout(5_000) { while (created.isEmpty()) delay(10) }
        withTimeout(5_000) { while (server.requestCount < 3) delay(10) }
        val permission = withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS)!! }
        assertEquals("/sessions", permission.path)
        val grant = withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS)!! }
        assertEquals("/sessions/new1/permissions", grant.path)
        assertTrue(grant.body.readUtf8().contains("\"mode\":\"full\""))
    }

    @Test
    fun `drafts reload from storage and clear once the send is acknowledged`() {
        val drafts = MemoryDrafts()
        withClient(drafts) { server, client ->
            client.link.value = LinkStatus.CONNECTED
            client.saveDraft(client.chat("s1"), "还没发出去的话")
            // 新连接实例模拟进程重启：草稿从存储恢复。
            val revived = AgentClient(client.config, drafts)
            try {
                withContext(Dispatchers.Main) {
                    assertEquals("还没发出去的话", revived.chat("s1").draft)
                }
            } finally {
                withContext(Dispatchers.Main) { revived.shutdown(); revived.awaitShutdown() }
            }
            // 排队发送被确认后，草稿与存储一起清空。
            server.enqueue(MockResponse().setBody("""{"items":[],"revision":1}"""))
            val session = client.chat("s1")
            session.running = true
            client.sendPrompt(session, "还没发出去的话")
            withTimeout(5_000) { while (session.sending) delay(10) }
            assertFalse(drafts.map.containsKey("${client.storageIdentity}/s1"))
        }
    }
}
