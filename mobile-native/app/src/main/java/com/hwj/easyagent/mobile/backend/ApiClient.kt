package com.hwj.easyagent.mobile.backend

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.io.IOException
import java.net.SocketTimeoutException
import java.net.URI
import java.util.concurrent.TimeUnit

class ApiException(message: String, val status: Int = 0) : Exception(message)

/** 对应 desktop-native/backend.go 的 backendClient：Bearer 鉴权的 REST 调用与 /ws 升级。 */
class ApiClient(val baseUrl: String, private val token: String) {
    private val http = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(45, TimeUnit.SECONDS)
        .writeTimeout(45, TimeUnit.SECONDS)
        .pingInterval(20, TimeUnit.SECONDS)
        .followRedirects(false)
        .build()

    suspend fun get(path: String): JsonElement = request("GET", path, null)

    suspend fun post(path: String, body: JsonElement? = null): JsonElement = request("POST", path, body)

    suspend fun patch(path: String, body: JsonElement): JsonElement = request("PATCH", path, body)

    suspend fun delete(path: String): JsonElement = request("DELETE", path, null)

    private suspend fun request(method: String, path: String, body: JsonElement?): JsonElement = withContext(Dispatchers.IO) {
        val builder = Request.Builder().url(baseUrl + path)
        if (token.isNotEmpty()) builder.header("Authorization", "Bearer $token")
        val payload = body?.let { wireJson.encodeToString(JsonElement.serializer(), it).toRequestBody(JSON) }
        when (method) {
            "GET" -> builder.get()
            "DELETE" -> builder.delete(payload)
            else -> builder.method(method, payload ?: "{}".toRequestBody(JSON))
        }
        try {
            http.newCall(builder.build()).execute().use { response ->
                val data = response.body?.string().orEmpty()
                if (!response.isSuccessful) throw ApiException(errorMessage(response.code, data), response.code)
                if (data.isBlank()) JsonNull else runCatching { wireJson.parseToJsonElement(data) }.getOrElse { throw ApiException("服务响应格式无效") }
            }
        } catch (e: SocketTimeoutException) {
            throw ApiException("服务处理超时，草稿已保留")
        } catch (e: IOException) {
            throw ApiException("无法连接服务，请检查主机地址和网络")
        }
    }

    private fun errorMessage(code: Int, data: String): String {
        if (code == 401 || code == 403) return "服务拒绝访问（HTTP $code），请检查认证或权限"
        val parsed = runCatching { wireJson.parseToJsonElement(data).obj() }.getOrNull()
        var message = parsed?.getString("error").orEmpty().ifEmpty { parsed?.getString("message").orEmpty() }
        if (message.isEmpty()) message = "HTTP $code"
        if (token.isNotEmpty()) message = message.replace(token, "[已隐藏]")
        return "$message（HTTP $code）"
    }

    fun openSocket(listener: WebSocketListener): WebSocket {
        val wsUrl = baseUrl.replaceFirst("http", "ws") + "/ws"
        val builder = Request.Builder().url(wsUrl)
        if (token.isNotEmpty()) builder.header("Authorization", "Bearer $token")
        return http.newWebSocket(builder.build(), listener)
    }

    companion object {
        private val JSON = "application/json; charset=utf-8".toMediaType()

        /** 与桌面端相同的地址校验：只接受不带凭据、查询参数和片段的 HTTP(S) 地址。 */
        fun normalize(raw: String): String {
            val trimmed = raw.trim().trimEnd('/')
            val url = trimmed.toHttpUrlOrNull()
            val hasUserInfo = runCatching { URI(trimmed).rawUserInfo != null }.getOrDefault(true)
            if (url == null || url.host.isEmpty() || hasUserInfo || url.query != null || url.fragment != null) {
                throw IllegalArgumentException("服务地址必须是无凭据、参数和片段的 HTTP(S) 地址")
            }
            return url.toString().trimEnd('/')
        }

        fun sessionPath(id: String): String = "/sessions/" + java.net.URLEncoder.encode(id, "UTF-8").replace("+", "%20")
    }
}
