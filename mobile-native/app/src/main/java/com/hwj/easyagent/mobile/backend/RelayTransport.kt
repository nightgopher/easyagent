package com.hwj.easyagent.mobile.backend

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString
import java.io.Closeable
import java.io.IOException
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Proxy
import java.net.ServerSocket
import java.net.Socket
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.time.Instant
import java.util.Base64
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.Semaphore
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager

/** Public routing and pinned identity only. Device tokens stay in the Keystore. */
@Serializable
data class RelayConfig(val relayUrl: String, val routeId: String, val fingerprint: String) {
    fun pin(): ByteArray {
        require(fingerprint.startsWith("sha256:")) { "配对证书指纹无效" }
        val bytes = runCatching { Base64.getDecoder().decode(fingerprint.removePrefix("sha256:")) }.getOrNull()
        require(bytes?.size == 32) { "配对证书指纹无效" }
        return requireNotNull(bytes)
    }
    val serverName: String get() = "ea-" + pin().take(16).joinToString("") { "%02x".format(it) } + ".invalid"
    val baseUrl: String get() = "https://$serverName"

    fun validate() {
        val url = relayUrl.toHttpUrl()
        require(url.isHttps && url.encodedPath == "/" && url.username.isEmpty() && url.password.isEmpty() && url.query == null && url.fragment == null) { "配对中继地址无效" }
        require(routeId.matches(Regex("[A-Za-z0-9_-]{43}"))) { "配对路由无效" }
        pin()
    }
}

@Serializable
data class RelayPairCode(
    val v: Int,
    @SerialName("relay_url") val relayUrl: String,
    @SerialName("route_id") val routeId: String,
    @SerialName("host_pubkey_fp") val fingerprint: String,
    @SerialName("pair_token") val pairToken: String,
    @SerialName("host_name") val hostName: String = "电脑",
    @SerialName("expires_at") val expiresAt: String,
) {
    val relay: RelayConfig get() = RelayConfig(relayUrl, routeId, fingerprint)
    companion object {
        fun parse(raw: String, now: Instant = Instant.now()): RelayPairCode {
            require(raw.length <= 8192) { "配对码过长" }
            val code = runCatching { wireJson.decodeFromString<RelayPairCode>(raw) }.getOrElse { throw IllegalArgumentException("这不是 EasyAgent 配对码") }
            require(code.v == 1) { "请更新 EasyAgent 后重新扫码" }
            code.relay.validate()
            require(code.pairToken.matches(Regex("[A-Za-z0-9_-]{43}"))) { "配对码无效" }
            val expiry = runCatching { Instant.parse(code.expiresAt) }.getOrNull()
            require(expiry != null && expiry.isAfter(now) && expiry.isBefore(now.plusSeconds(600))) { "配对码已过期，请在电脑上重新生成" }
            return code
        }
    }

    suspend fun claim(deviceName: String): HostConfig {
        val api = ApiClient(relay.baseUrl, "", relay)
        try {
            val result = api.post("/pair/claim", buildJsonObject { put("pair_token", pairToken); put("name", deviceName.take(64)) }).obj()
            val token = result.getString("device_token")
            check(token.matches(Regex("[A-Za-z0-9_-]{43}"))) { "电脑返回的设备凭据无效，请重新扫码" }
            return HostConfig(relay.baseUrl, token, name = hostName.take(128), relay = relay)
        } finally { api.close() }
    }
}

internal class PinnedHostTrust(private val pin: ByteArray) : X509TrustManager {
    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) = throw CertificateException("client certificates unsupported")
    override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        val cert = chain?.firstOrNull() ?: throw CertificateException("missing host certificate")
        cert.checkValidity()
        val actual = MessageDigest.getInstance("SHA-256").digest(cert.publicKey.encoded)
        if (!MessageDigest.isEqual(pin, actual)) throw CertificateException("电脑身份已变化，请重新扫码")
    }
}

/** App-local CONNECT proxy. It sees only inner TLS records; it never accepts an
 * arbitrary destination or sends credentials in the outer WebSocket handshake. */
internal class RelayTransport(private val config: RelayConfig, private val outer: OkHttpClient = defaultOuter()) : Closeable {
    private val closed = AtomicBoolean()
    private val sockets = ConcurrentHashMap<Socket, WebSocket>()
    private val accepted = ConcurrentHashMap.newKeySet<Socket>()
    private val slots = Semaphore(16)
    private val workers = Executors.newCachedThreadPool { task -> Thread(task, "easyagent-relay").apply { isDaemon = true } }
    private val listener = ServerSocket()
    val proxy: Proxy

    init {
        config.validate()
        listener.bind(InetSocketAddress(InetAddress.getByName("127.0.0.1"), 0), 16)
        proxy = Proxy(Proxy.Type.HTTP, InetSocketAddress("127.0.0.1", listener.localPort))
        workers.execute {
            while (!closed.get()) {
                val socket = try { listener.accept() } catch (_: IOException) { break }
                if (!slots.tryAcquire()) { socket.close(); continue }
                accepted.add(socket)
                try {
                    workers.execute { try { bridge(socket) } finally { slots.release() } }
                } catch (_: java.util.concurrent.RejectedExecutionException) {
                    accepted.remove(socket); socket.close(); slots.release()
                }
            }
        }
    }

    fun configure(builder: OkHttpClient.Builder): OkHttpClient.Builder {
        val trust = PinnedHostTrust(config.pin())
        val tls = SSLContext.getInstance("TLS").apply { init(null, arrayOf(trust), null) }
        // The standard hostname verifier still checks the certificate SAN.
        return builder.proxy(proxy).sslSocketFactory(tls.socketFactory, trust)
    }

    private fun bridge(socket: Socket) {
        var ws: WebSocket? = null
        try {
            socket.use {
                socket.soTimeout = 15_000
                val input = socket.getInputStream()
                val header = StringBuilder()
                while (!header.endsWith("\r\n\r\n")) {
                    val byte = input.read()
                    if (byte < 0 || header.length >= 8192) throw IOException("invalid CONNECT")
                    header.append(byte.toChar())
                }
                if (header.lineSequence().first().trim() != "CONNECT ${config.serverName}:443 HTTP/1.1") throw IOException("unexpected CONNECT target")
                val ready = CountDownLatch(1)
                val opened = AtomicBoolean()
                val url = config.relayUrl.toHttpUrl().newBuilder().addPathSegment("client").addQueryParameter("route_id", config.routeId).build()
                ws = outer.newWebSocket(Request.Builder().url(url).build(), object : WebSocketListener() {
                    override fun onOpen(webSocket: WebSocket, response: Response) { opened.set(true); ready.countDown() }
                    override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                        if (bytes.size > 65536) { webSocket.cancel(); socket.close(); return }
                        try { socket.getOutputStream().write(bytes.toByteArray()) } catch (_: IOException) { webSocket.cancel() }
                    }
                    override fun onMessage(webSocket: WebSocket, text: String) { webSocket.cancel(); socket.close() }
                    override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) { ready.countDown(); socket.close() }
                    override fun onClosing(webSocket: WebSocket, code: Int, reason: String) { webSocket.close(code, null); socket.close() }
                })
                sockets[socket] = requireNotNull(ws)
                if (closed.get() || !ready.await(15, TimeUnit.SECONDS) || !opened.get()) throw IOException("relay unavailable")
                socket.getOutputStream().write("HTTP/1.1 200 Connection Established\r\n\r\n".toByteArray())
                socket.soTimeout = 0
                val bytes = ByteArray(32 shl 10)
                while (!closed.get()) {
                    val n = input.read(bytes)
                    if (n < 0) break
                    val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(20)
                    while (requireNotNull(ws).queueSize() > 256 shl 10) {
                        if (closed.get() || socket.isClosed || System.nanoTime() > deadline) throw IOException("relay backpressure timeout")
                        Thread.sleep(10)
                    }
                    if (!requireNotNull(ws).send(bytes.toByteString(0, n))) break
                }
            }
        } catch (_: Exception) {
            // The inner HTTP/TLS client reports a bounded, credential-free error.
        } finally {
            sockets.remove(socket)
            accepted.remove(socket)
            ws?.cancel()
            socket.close()
        }
    }

    override fun close() {
        if (!closed.compareAndSet(false, true)) return
        listener.close()
        sockets.forEach { (socket, ws) -> ws.cancel(); socket.close() }
        accepted.forEach { it.close() }
        accepted.clear()
        sockets.clear()
        workers.shutdownNow()
        outer.dispatcher.cancelAll()
        outer.connectionPool.evictAll()
        outer.dispatcher.executorService.shutdown()
    }

    companion object {
        private fun defaultOuter() = OkHttpClient.Builder().connectTimeout(15, TimeUnit.SECONDS).readTimeout(0, TimeUnit.SECONDS)
            .writeTimeout(20, TimeUnit.SECONDS).pingInterval(20, TimeUnit.SECONDS).followRedirects(false).build()
    }
}
