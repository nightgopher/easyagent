package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.HostConfig
import com.hwj.easyagent.mobile.backend.HostConnections
import com.hwj.easyagent.mobile.backend.LinkStatus
import com.hwj.easyagent.mobile.backend.SettingsStore
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.setMain
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.junit.Assert.*
import org.junit.Test
import java.util.Collections
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger

@OptIn(ExperimentalCoroutinesApi::class)
class HostConnectionsTest {
    private class Host(val id: String) {
        val sockets = AtomicInteger()
        val authorizations = Collections.synchronizedList(mutableListOf<Pair<String, String?>>())
        @Volatile var socket: WebSocket? = null
        val server = MockWebServer().apply {
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse {
                    authorizations.add(request.path.orEmpty() to request.getHeader("Authorization"))
                    return if (request.path == "/ws") {
                        sockets.incrementAndGet()
                        MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                            override fun onOpen(webSocket: WebSocket, response: Response) { socket = webSocket }
                            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) { webSocket.close(code, reason) }
                        })
                    } else {
                        MockResponse().setBody("""[{"id":"shared","title":"$id session"}]""")
                    }
                }
            }
            start()
        }
        val config get() = HostConfig(server.url("/").toString().trimEnd('/'), "$id-token", id, id)
    }

    private suspend fun eventually(check: () -> Boolean) {
        withTimeout(8_000) { while (!check()) delay(10) }
    }

    private fun withHosts(block: suspend (Host, Host, SettingsStore, MemoryPreferences, HostConnections) -> Unit) = runBlocking {
        val main = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val a = Host("a")
        val b = Host("b")
        Dispatchers.setMain(main)
        val prefs = MemoryPreferences()
        val settings = SettingsStore(prefs, TestTokenCipher)
        settings.save(a.config)
        settings.save(b.config)
        val connections = withContext(main) { HostConnections(settings) }
        try {
            withContext(main) {
                eventually { connections.clientFor("a")?.link?.value == LinkStatus.CONNECTED && connections.clientFor("b")?.link?.value == LinkStatus.CONNECTED }
                block(a, b, settings, prefs, connections)
            }
        } finally {
            withContext(main) { connections.shutdown() }
            Dispatchers.resetMain()
            main.close()
            a.server.shutdown()
            b.server.shutdown()
        }
    }

    @Test
    fun `switching preserves separate clients sessions drafts and credentials even for identical session IDs`() = withHosts { a, b, settings, _, connections ->
        val ca = connections.clientFor("a")!!
        val cb = connections.clientFor("b")!!
        eventually { ca.sessions.value.isNotEmpty() && cb.sessions.value.isNotEmpty() }
        val sa = ca.chat("shared")
        val sb = cb.chat("shared")
        sa.draft = "draft for a"
        sb.draft = "draft for b"
        sa.running = true
        a.socket!!.send("""{"type":"event","session_id":"shared","seq":1,"event":{"type":"text_delta","text_delta":"only a"}}""")
        eventually { sa.messages.isNotEmpty() }
        connections.select("a")
        connections.select("b")
        connections.select("a")
        assertSame(ca, connections.client.value)
        assertSame(cb, connections.clientFor("b"))
        assertEquals("a", settings.load().activeId)
        assertEquals("draft for a", sa.draft)
        assertEquals("draft for b", sb.draft)
        assertEquals("only a", sa.messages.single().text)
        assertTrue(sb.messages.isEmpty())
        assertEquals("a session", ca.sessions.value.single().title)
        assertEquals("b session", cb.sessions.value.single().title)
        assertEquals(1, a.sockets.get())
        assertEquals(1, b.sockets.get())
        for (host in listOf(a, b)) {
            assertTrue(host.authorizations.any { it.first == "/ws" })
            assertTrue(host.authorizations.any { it.first == "/sessions" })
            assertTrue(host.authorizations.all { it.second == "Bearer ${host.id}-token" })
        }
    }

    @Test
    fun `inactive host reconnects independently and removal stops only its connection`() = withHosts { a, b, _, _, connections ->
        val ca = connections.clientFor("a")!!
        val cb = connections.clientFor("b")!!
        ca.chat("shared").draft = "keep through reconnect"
        assertSame(cb, connections.client.value)
        a.socket!!.close(1012, "restart")
        eventually { a.sockets.get() == 2 && ca.link.value == LinkStatus.CONNECTED }
        assertSame(cb, connections.client.value)
        assertEquals(LinkStatus.CONNECTED, cb.link.value)
        assertEquals(1, b.sockets.get())
        assertEquals("keep through reconnect", ca.chat("shared").draft)
        connections.remove("a")
        assertNull(connections.clientFor("a"))
        assertSame(cb, connections.client.value)
        assertEquals(LinkStatus.DISCONNECTED, ca.link.value)
        delay(1_200)
        assertEquals(2, a.sockets.get())
        connections.remove("b")
        assertNull(connections.client.value)
        assertTrue(connections.state.value.hosts.isEmpty())
    }

    @Test
    fun `background acceptance clears only the submitted hosts unchanged draft`() = withHosts { a, _, _, _, connections ->
        val sa = connections.clientFor("a")!!.chat("shared")
        val sb = connections.clientFor("b")!!.chat("shared")
        sa.draft = "submitted"
        sa.sentDraft = "submitted"
        sa.pendingRequest = "request-a"
        sa.sending = true
        sb.draft = "other host draft"
        a.socket!!.send("""{"type":"accepted","session_id":"shared","request_id":"request-a"}""")
        eventually { !sa.sending }
        assertEquals("", sa.draft)
        assertEquals("other host draft", sb.draft)
        sa.draft = "newer draft"
        sa.pendingRequest = "request-a2"
        sa.sending = true
        a.socket!!.send("""{"type":"accepted","session_id":"shared","request_id":"request-a2"}""")
        eventually { !sa.sending }
        assertEquals("newer draft", sa.draft)
    }

    @Test
    fun `name edits preserve connection but changed endpoint cannot inherit old host state`() = withHosts { a, _, _, _, connections ->
        val original = connections.clientFor("a")!!
        original.chat("shared").draft = "belongs to old endpoint"
        connections.save(a.config.copy(name = "Renamed"))
        assertSame(original, connections.clientFor("a"))
        val other = Host("other")
        try {
            connections.save(other.config.copy(id = "a"))
            val replaced = connections.clientFor("a")!!
            assertNotSame(original, replaced)
            assertEquals(LinkStatus.DISCONNECTED, original.link.value)
            assertEquals("", replaced.chat("shared").draft)
            assertEquals("b", connections.clientFor("b")!!.config.id)
        } finally {
            connections.remove("a")
            other.server.shutdown()
        }
    }

    @Test
    fun `failed save or selection preserves active connection and all local state`() = withHosts { a, _, _, prefs, connections ->
        val active = connections.client.value
        prefs.failCommit = true
        assertThrows(IllegalStateException::class.java) { connections.select("a") }
        assertThrows(IllegalStateException::class.java) { connections.save(a.config.copy(token = "new-token")) }
        assertThrows(IllegalStateException::class.java) { connections.remove("b") }
        assertSame(active, connections.client.value)
        assertEquals("b", connections.state.value.activeId)
        assertEquals(2, connections.state.value.hosts.size)
    }

    @Test
    fun `unreadable host credentials never become anonymous network requests`() = withHosts { _, b, settings, prefs, connections ->
        connections.shutdown()
        val before = b.server.requestCount
        prefs.edit().putString("token:b", "broken").commit()
        val restarted = HostConnections(settings)
        try {
            assertNull(restarted.client.value)
            assertNull(restarted.clientFor("b"))
            assertNull(restarted.state.value.hosts.last().token)
            restarted.select("a")
            eventually { restarted.client.value?.link?.value == LinkStatus.CONNECTED }
            assertEquals(before, b.server.requestCount)
        } finally {
            restarted.shutdown()
        }
    }
}
