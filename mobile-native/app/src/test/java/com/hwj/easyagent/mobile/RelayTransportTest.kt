package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.*
import java.io.File
import java.nio.file.Files
import java.security.KeyStore
import java.security.cert.CertificateFactory
import java.util.Base64
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLContext
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509TrustManager
import kotlinx.serialization.json.*
import okhttp3.*
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import org.junit.Assert.*
import org.junit.Test

class RelayTransportTest {
    @Test fun realGoRelayPairsStreamsRejectsWrongPinAndReconnects() {
        val root = generateSequence(File(requireNotNull(System.getProperty("user.dir")))) { it.parentFile }.first { File(it, "go.mod").exists() }
        val output = Files.createTempFile("easyagent-relay-", ".json").toFile()
        val log = Files.createTempFile("easyagent-relay-", ".log").toFile()
        val process = ProcessBuilder("go", "run", "./internal/relay/testdata/interop", output.path).directory(root).redirectError(log).redirectOutput(log).start()
        try {
            val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(90)
            while (output.length() == 0L && process.isAlive && System.nanoTime() < deadline) Thread.sleep(50)
            assertTrue("Go relay fixture did not start: ${log.readText()}", output.length() > 0)
            val fixture = wireJson.parseToJsonElement(output.readText()).obj()
            val code = RelayPairCode.parse(fixture["code"].toString())
            val cert = fixture.getString("outer_certificate").takeIf { it.isNotBlank() }?.let { CertificateFactory.getInstance("X.509").generateCertificate(Base64.getDecoder().decode(it).inputStream()) }
            val store = KeyStore.getInstance(KeyStore.getDefaultType()).apply { load(null); if (cert != null) setCertificateEntry("fixture", cert) }
            val factory = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm()).apply { init(store) }
            val trust = factory.trustManagers.filterIsInstance<X509TrustManager>().single()
            fun outer(): OkHttpClient {
                if (cert == null) return OkHttpClient.Builder().pingInterval(20, TimeUnit.SECONDS).build()
                val tls = SSLContext.getInstance("TLS").apply { init(null, arrayOf(trust), null) }
                return OkHttpClient.Builder().sslSocketFactory(tls.socketFactory, trust).pingInterval(20, TimeUnit.SECONDS).build()
            }
            RelayTransport(code.relay, outer()).use { transport ->
                val http = transport.configure(OkHttpClient.Builder()).callTimeout(15, TimeUnit.SECONDS).build()
                val base = code.relay.baseUrl
                fun get(path: String, token: String = ""): Response = http.newCall(Request.Builder().url(base + path).header("Authorization", "Bearer $token").build()).execute()
                get("/sessions").use { assertEquals(401, it.code) }
                val body = buildJsonObject { put("pair_token", code.pairToken); put("name", "Android fixture") }.toString().toRequestBody("application/json".toMediaType())
                val pairRequest = Request.Builder().url("$base/pair/claim").post(body).build()
                val token = http.newCall(pairRequest).execute().use {
                    assertEquals(201, it.code)
                    wireJson.parseToJsonElement(it.body!!.string()).obj().getString("device_token")
                }
                http.newCall(pairRequest).execute().use { assertEquals(401, it.code) }
                get("/sessions", token).use { assertEquals(200, it.code) }
                get("/remote-access", token).use { assertEquals(403, it.code) }
                val payload = "attachment-content\n".repeat(65536).toByteArray()
                get("/attachment", token).use { assertArrayEquals(payload, it.body!!.bytes()) }
                http.newCall(Request.Builder().url("$base/echo").header("Authorization", "Bearer $token").post(payload.toRequestBody()).build()).execute().use { assertArrayEquals(payload, it.body!!.bytes()) }
                val echoed = CountDownLatch(1)
                val disconnected = CountDownLatch(1)
                val ws = http.newWebSocket(Request.Builder().url(base.replaceFirst("https", "wss") + "/ws").header("Authorization", "Bearer $token").build(), object : WebSocketListener() {
                    override fun onOpen(webSocket: WebSocket, response: Response) { webSocket.send("private chat") }
                    override fun onMessage(webSocket: WebSocket, text: String) { if (text == "private chat") echoed.countDown() }
                    override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) { disconnected.countDown() }
                    override fun onClosing(webSocket: WebSocket, code: Int, reason: String) { disconnected.countDown() }
                })
                assertTrue("chat did not echo over inner TLS", echoed.await(15, TimeUnit.SECONDS))
                fun control(path: String) = OkHttpClient().newCall(Request.Builder().url(fixture.getString("control_url") + path).header("Authorization", "Bearer " + fixture.getString("control_token")).build()).execute().use { assertEquals(200, it.code) }
                control("/restart")
                assertTrue("broker restart must close the old chat", disconnected.await(10, TimeUnit.SECONDS))
                var recovered = false
                repeat(25) {
                    if (!recovered) {
                        Thread.sleep(200)
                        recovered = runCatching { get("/sessions", token).use { it.code == 200 } }.getOrDefault(false)
                    }
                }
                assertTrue("host must recover the same identity after broker restart", recovered)
                control("/revoke")
                get("/sessions", token).use { assertEquals(401, it.code) }
                ws.cancel()
                http.dispatcher.cancelAll(); http.connectionPool.evictAll(); http.dispatcher.executorService.shutdown()
            }
            val wrong = code.relay.copy(fingerprint = "sha256:" + Base64.getEncoder().encodeToString(ByteArray(32)))
            RelayTransport(wrong, outer()).use { transport ->
                val http = transport.configure(OkHttpClient.Builder()).callTimeout(10, TimeUnit.SECONDS).build()
                assertThrows(java.io.IOException::class.java) { http.newCall(Request.Builder().url(wrong.baseUrl + "/health").build()).execute().close() }
                http.dispatcher.cancelAll(); http.connectionPool.evictAll(); http.dispatcher.executorService.shutdown()
            }
        } finally {
            process.outputStream.close()
            if (!process.waitFor(10, TimeUnit.SECONDS)) process.destroy()
            output.delete(); log.delete()
        }
    }

    @Test fun rejectsExpiredAndInsecurePairingCodes() {
        val future = java.time.Instant.now().plusSeconds(300).toString()
        val valid = buildJsonObject {
            put("v", 1); put("relay_url", "https://relay.example"); put("route_id", "a".repeat(43))
            put("host_pubkey_fp", "sha256:" + Base64.getEncoder().encodeToString(ByteArray(32)))
            put("pair_token", "b".repeat(43)); put("host_name", "computer"); put("expires_at", future)
        }.toString()
        assertEquals("ea-00000000000000000000000000000000.invalid", RelayPairCode.parse(valid).relay.serverName)
        assertThrows(IllegalArgumentException::class.java) { RelayPairCode.parse(valid.replace("https://", "http://")) }
        assertThrows(IllegalArgumentException::class.java) { RelayPairCode.parse(valid.replace(future, "2020-01-01T00:00:00Z")) }
        assertThrows(IllegalArgumentException::class.java) { RelayPairCode.parse(valid.replace("https://relay.example", "https://relay.example/?token=secret")) }
    }
}
