package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.HostConfig
import com.hwj.easyagent.mobile.backend.LinkStatus
import com.hwj.easyagent.mobile.backend.MessageQueue
import com.hwj.easyagent.mobile.backend.QueueItem
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
    private fun withClient(block: suspend (MockWebServer, AgentClient) -> Unit) = runBlocking {
        val main = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val server = MockWebServer()
        server.start()
        Dispatchers.setMain(main)
        val client = AgentClient(HostConfig(server.url("/").toString().trimEnd('/'), ""))
        try {
            withContext(main) { block(server, client) }
        } finally {
            withContext(main) { client.shutdown() }
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
        assertEquals(1, session.draftClearCount)
        assertEquals("", session.pendingRequest)
    }

    @Test
    fun `failed queue submission preserves retry identity and draft`() = withClient { server, client ->
        server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"queue full"}"""))
        client.link.value = LinkStatus.CONNECTED
        val session = client.chat("s1")
        session.running = true
        client.sendPrompt(session, "keep this draft")
        val requestId = session.pendingRequest
        withTimeout(5_000) { while (client.error.value == null) delay(10) }
        assertFalse(session.sending)
        assertTrue(requestId.isNotEmpty())
        assertEquals(requestId, session.pendingRequest)
        assertEquals("keep this draft", session.sentDraft)
        assertEquals(0, session.draftClearCount)
    }
}
