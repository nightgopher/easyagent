package com.hwj.easyagent.mobile

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import com.hwj.easyagent.mobile.backend.*
import com.hwj.easyagent.mobile.ui.ConnectionScreen
import java.net.Socket
import java.security.MessageDigest
import java.time.Instant
import java.util.Base64
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.*
import kotlinx.serialization.encodeToString
import okhttp3.OkHttpClient
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import okio.ByteString
import okio.ByteString.Companion.toByteString
import org.junit.Assert.*
import org.junit.Test
import org.junit.Rule
import org.junit.runner.RunWith

/** Android's TLS provider enforces Main-thread policy; JVM tests cannot catch it. */
@RunWith(AndroidJUnit4::class)
class RelayPairingTest {
    @Test fun claimOnMainSavesEncryptedCredentialAndReopensChat() = runBlocking {
        RelayFixture().use { fixture ->
            val store = SettingsStore(InstrumentationRegistry.getInstrumentation().targetContext)
            val host = withContext(Dispatchers.Main) {
                fixture.code.claim("Android test phone", fixture.api()).also { store.save(it) }
            }
            try {
                val restored = store.load().hosts.single { it.id == host.id }
                assertEquals(host, restored)
                val prefs = InstrumentationRegistry.getInstrumentation().targetContext
                    .getSharedPreferences("easyagent-host", 0).all.toString()
                assertTrue(prefs.contains("token:"))
                assertFalse("Device credentials must not be stored in plaintext", prefs.contains(requireNotNull(host.token)))
                // A save retry reuses the issued credential, without consuming the code again.
                withContext(Dispatchers.Main) { store.save(host) }
                assertEquals(1, fixture.claims.get())
                val api = fixture.api(requireNotNull(restored.token))
                val closed = CountDownLatch(1)
                try {
                    assertTrue(withContext(Dispatchers.Main) { api.get("/sessions").toString().contains("saved conversation") })
                    val echo = CountDownLatch(1)
                    api.openSocket(object : WebSocketListener() {
                        override fun onOpen(webSocket: WebSocket, response: Response) { webSocket.send("private chat") }
                        override fun onMessage(webSocket: WebSocket, text: String) { if (text == "private chat") echo.countDown() }
                        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) { closed.countDown() }
                        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) { closed.countDown() }
                    })
                    assertTrue("Saved credential must reopen chat over pinned TLS", echo.await(10, TimeUnit.SECONDS))
                } finally {
                    withContext(Dispatchers.Main) { api.close() }
                }
                // cancelAll() closes sockets immediately, but OkHttp releases an
                // active WebSocket's pool entry after its reader/callback returns.
                assertTrue("Chat socket did not close", closed.await(10, TimeUnit.SECONDS))
                assertTrue("Network callbacks did not finish", api.http.dispatcher.executorService.awaitTermination(10, TimeUnit.SECONDS))
                assertEquals(0, api.http.connectionPool.connectionCount())
                assertTrue(api.http.dispatcher.executorService.isShutdown)
            } finally {
                withContext(Dispatchers.Main) { store.remove(host.id) }
            }
        }
    }

    @Test fun cancelledMainCoroutineStillClosesTlsConnections() = runBlocking {
        RelayFixture().use { fixture ->
            val api = fixture.api()
            withContext(Dispatchers.Main) { api.get("/health") }
            assertTrue(api.http.connectionPool.connectionCount() > 0)
            val started = CompletableDeferred<Unit>()
            val owner = launch(Dispatchers.Main) {
                try { started.complete(Unit); awaitCancellation() }
                finally { api.close() }
            }
            started.await()
            owner.cancelAndJoin()
            assertEquals(0, api.http.connectionPool.connectionCount())
            assertTrue(api.http.dispatcher.executorService.isShutdown)
        }
    }
}

@RunWith(AndroidJUnit4::class)
class RelayPairingUiTest {
    @get:Rule val ui = createComposeRule()

    private fun waitForRetry(fixture: RelayFixture, failure: AtomicReference<Throwable?>) {
        try {
            // The TLS request has a 45 s read timeout plus connection setup;
            // don't time out the UI assertion before the request can finish.
            ui.waitUntil(60_000) {
                failure.get() != null || ui.onAllNodesWithText("重试保存").fetchSemanticsNodes().isNotEmpty()
            }
        } catch (e: ComposeTimeoutException) {
            throw AssertionError("Pairing did not finish: ${fixture.diagnostics()}", e)
        }
        failure.get()?.let { throw AssertionError("Pairing failed: ${fixture.diagnostics()}", it) }
    }

    @Test fun secureStorageRetryDoesNotPairSamePhoneTwice() {
        RelayFixture().use { fixture ->
            val saved = AtomicReference<HostConfig?>()
            val saves = AtomicInteger()
            val cancelled = AtomicBoolean()
            val failure = AtomicReference<Throwable?>()
            val store = SettingsStore(InstrumentationRegistry.getInstrumentation().targetContext)
            try {
                ui.setContent {
                    MaterialTheme {
                        ConnectionScreen(null, true, { cancelled.set(true) }, { host ->
                            if (saves.incrementAndGet() == 1) error("Simulated Keystore write failure")
                            store.save(host)
                            saved.set(host)
                        }, {}, claim = {
                            try { it.claim("Android test phone", fixture.api()) }
                            catch (e: Exception) { failure.set(e); throw e }
                        })
                    }
                }
                ui.onNodeWithText("粘贴配对码").performClick()
                ui.onNodeWithText("配对码").performTextReplacement(wireJson.encodeToString(fixture.code))
                ui.onNodeWithText("配对并连接").performScrollTo().performClick()
                waitForRetry(fixture, failure)
                ui.onNodeWithText("电脑已完成配对，但手机保存凭据失败，请点“重试保存”").assertExists()
                ui.onNodeWithText("扫码配对电脑").assertIsNotEnabled()
                ui.onNodeWithText("填写主机地址").assertIsNotEnabled()
                ui.onNodeWithContentDescription("返回").performClick()
                ui.onNodeWithText("连接尚未保存").assertExists()
                ui.onNodeWithText("继续保存").performClick()
                assertFalse(cancelled.get())
                ui.onNodeWithText("重试保存").performScrollTo().performClick()
                ui.waitUntil(10_000) { saved.get() != null }
                assertEquals(2, saves.get())
                assertEquals(1, fixture.claims.get())
                assertEquals(saved.get(), store.load().hosts.single { it.id == saved.get()?.id })
            } finally {
                saved.get()?.let { store.remove(it.id) }
            }
        }
    }

    @Test fun userCanLeaveAfterAClaimedCredentialCouldNotBeSaved() {
        RelayFixture().use { fixture ->
            val cancelled = AtomicBoolean()
            val failure = AtomicReference<Throwable?>()
            ui.setContent {
                MaterialTheme {
                    ConnectionScreen(null, true, { cancelled.set(true) }, { error("Simulated Keystore write failure") }, {},
                        claim = {
                            try { it.claim("Android test phone", fixture.api()) }
                            catch (e: Exception) { failure.set(e); throw e }
                        })
                }
            }
            ui.onNodeWithText("粘贴配对码").performClick()
            ui.onNodeWithText("配对码").performTextReplacement(wireJson.encodeToString(fixture.code))
            ui.onNodeWithText("配对并连接").performScrollTo().performClick()
            waitForRetry(fixture, failure)
            ui.onNodeWithContentDescription("返回").performClick()
            ui.onNodeWithText("仍要返回").performClick()
            assertTrue(cancelled.get())
            assertEquals(1, fixture.claims.get())
        }
    }

    @Test fun userCanReturnWhilePairingIsInProgress() {
        val entered = AtomicBoolean()
        val cancelled = AtomicBoolean()
        val gate = CompletableDeferred<HostConfig>()
        val code = RelayPairCode(1, "https://relay.example", "r".repeat(43),
            "sha256:" + Base64.getEncoder().encodeToString(ByteArray(32)), "p".repeat(43),
            "Fixture computer", Instant.now().plusSeconds(300).toString())
        ui.setContent {
            MaterialTheme {
                ConnectionScreen(null, true, { cancelled.set(true) }, {}, {}, claim = { entered.set(true); gate.await() })
            }
        }
        ui.onNodeWithText("粘贴配对码").performClick()
        ui.onNodeWithText("配对码").performTextReplacement(wireJson.encodeToString(code))
        ui.onNodeWithText("配对并连接").performScrollTo().performClick()
        ui.waitUntil(10_000) { entered.get() }
        ui.onNodeWithContentDescription("返回").assertIsEnabled().performClick()
        ui.onNodeWithText("配对正在进行").assertExists()
        ui.onNodeWithText("继续等待").performClick()
        assertFalse(cancelled.get())
        ui.onNodeWithContentDescription("返回").performClick()
        ui.onNodeWithText("仍要返回").performClick()
        assertTrue(cancelled.get())
        gate.cancel()
    }
}

/** Ephemeral local WSS relay forwarding inner TLS bytes. No public service or user data. */
private class RelayFixture : AutoCloseable {
    private val inner = MockWebServer()
    private val outer = MockWebServer()
    private val workers = Executors.newCachedThreadPool()
    private val sockets = ConcurrentHashMap.newKeySet<Socket>()
    private val key = HeldCertificate.Builder().ecdsa256().build().keyPair
    private val fingerprint = "sha256:" + Base64.getEncoder().encodeToString(MessageDigest.getInstance("SHA-256").digest(key.public.encoded))
    private val outerCert = HeldCertificate.Builder().commonName("localhost").addSubjectAlternativeName("localhost").build()
    private val outerTrust = HandshakeCertificates.Builder().addTrustedCertificate(outerCert.certificate).build()
    val claims = AtomicInteger()
    private val outerRequests = AtomicInteger()
    private val outerOpened = AtomicInteger()
    private val upstreamFrames = AtomicInteger()
    private val downstreamFrames = AtomicInteger()
    private val tunnelError = AtomicReference<String?>()
    val code: RelayPairCode

    init {
        val name = RelayConfig("https://localhost", "r".repeat(43), fingerprint).serverName
        val cert = HeldCertificate.Builder().keyPair(key).commonName(name).addSubjectAlternativeName(name).build()
        inner.useHttps(HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(), false)
        inner.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse = when {
                request.path == "/health" -> MockResponse().setBody("{}")
                request.path == "/pair/claim" && claims.incrementAndGet() == 1 ->
                    MockResponse().setResponseCode(201).setBody("{\"device_token\":\"${"d".repeat(43)}\"}")
                request.getHeader("Authorization") != "Bearer ${"d".repeat(43)}" -> MockResponse().setResponseCode(401)
                request.path == "/ws" -> MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                    override fun onMessage(webSocket: WebSocket, text: String) { webSocket.send(text) }
                })
                else -> MockResponse().setBody("[{\"id\":\"fixture\",\"title\":\"saved conversation\"}]")
            }
        }
        inner.start()
        outer.useHttps(HandshakeCertificates.Builder().heldCertificate(outerCert).build().sslSocketFactory(), false)
        outer.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                outerRequests.incrementAndGet()
                return MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                    private lateinit var socket: Socket
                    override fun onOpen(webSocket: WebSocket, response: Response) {
                        outerOpened.incrementAndGet()
                        socket = Socket("127.0.0.1", inner.port)
                        sockets.add(socket)
                        workers.execute {
                            try {
                                val buffer = ByteArray(16384)
                                while (true) {
                                    val count = socket.getInputStream().read(buffer)
                                    if (count < 0) break
                                    downstreamFrames.incrementAndGet()
                                    if (!webSocket.send(buffer.toByteString(0, count))) break
                                }
                            } catch (_: java.io.IOException) {
                            } finally { socket.close(); sockets.remove(socket); webSocket.close(1000, "tunnel closed") }
                        }
                    }
                    override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                        upstreamFrames.incrementAndGet()
                        try { socket.getOutputStream().write(bytes.toByteArray()) }
                        catch (e: java.io.IOException) { tunnelError.set(e.javaClass.simpleName); webSocket.close(1011, "tunnel failed") }
                    }
                    override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) { tunnelError.set(t.javaClass.simpleName); if (::socket.isInitialized) socket.close() }
                    override fun onClosing(webSocket: WebSocket, code: Int, reason: String) { socket.close(); webSocket.close(code, reason) }
                })
            }
        }
        outer.start()
        code = RelayPairCode(1, outer.url("/").toString().trimEnd('/'), "r".repeat(43), fingerprint, "p".repeat(43), "Fixture computer", Instant.now().plusSeconds(300).toString())
    }

    fun api(token: String = ""): ApiClient {
        val client = OkHttpClient.Builder().sslSocketFactory(outerTrust.sslSocketFactory(), outerTrust.trustManager).build()
        return ApiClient(code.relay.baseUrl, token, RelayTransport(code.relay, client))
    }

    fun diagnostics(): String = "outerRequests=${outerRequests.get()}, outerOpened=${outerOpened.get()}, " +
        "upstreamFrames=${upstreamFrames.get()}, downstreamFrames=${downstreamFrames.get()}, " +
        "claims=${claims.get()}, tunnelError=${tunnelError.get()}"

    override fun close() {
        sockets.forEach { it.close() }
        workers.shutdownNow()
        outer.shutdown()
        inner.shutdown()
    }
}
