package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.ApiException
import com.hwj.easyagent.mobile.backend.HostConfig
import com.hwj.easyagent.mobile.backend.wireJson
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.setMain
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.util.concurrent.Executors

@OptIn(ExperimentalCoroutinesApi::class)
class LoopClientTest {
    private fun withClient(block: suspend (MockWebServer, AgentClient) -> Unit) = runBlocking {
        val main = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val server = MockWebServer()
        server.start()
        Dispatchers.setMain(main)
        val client = AgentClient(HostConfig(server.url("/").toString().trimEnd('/'), ""))
        try {
            withContext(main) { block(server, client) }
        } finally {
            withContext(main) { client.shutdown(); client.awaitShutdown() }
            Dispatchers.resetMain()
            main.close()
            server.shutdown()
        }
    }

    private val active = """{"loop":{"prompt":"检查部署","interval_seconds":900,"started_at":"2026-10-22T10:00:00+08:00","expires_at":"2026-10-23T10:00:00+08:00","pending":false}}"""

    @Test
    fun `loop status parses active and empty`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("""{"loop":null}"""))
        server.enqueue(MockResponse().setBody(active))
        assertNull(client.loop("s1"))
        val loop = client.loop("s1")!!
        assertEquals("检查部署", loop.prompt)
        assertEquals(900, loop.intervalSeconds)
        assertEquals("", loop.lastRunAt)
        assertEquals("/sessions/s1/loop", server.takeRequest().path)
    }

    @Test
    fun `start sends the slash command and confirms the loop exists`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("""{"output":"🔄 Loop scheduled!"}"""))
        server.enqueue(MockResponse().setBody(active))
        client.startLoop("s1", 15, "检查\n部署", 24)
        val command = server.takeRequest()
        assertEquals("/sessions/s1/command", command.path)
        val body = wireJson.parseToJsonElement(command.body.readUtf8()).jsonObject
        assertEquals("/loop 15m 检查 部署 --expires 24h", body["command"]!!.jsonPrimitive.content)
    }

    @Test
    fun `refused start reports the host reply`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("""{"output":"Minimum loop interval is 1 minute"}"""))
        server.enqueue(MockResponse().setBody("""{"loop":null}"""))
        try {
            client.startLoop("s1", 15, "x", 1)
            fail("expected refusal")
        } catch (e: ApiException) {
            assertTrue(e.message!!.contains("Minimum loop interval"))
        }
    }

    @Test
    fun `stop deletes the loop`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("""{"stopped":true}"""))
        client.stopLoop("s1")
        val request = server.takeRequest()
        assertEquals("DELETE", request.method)
        assertEquals("/sessions/s1/loop", request.path)
    }
}
