package com.hwj.easyagent.mobile.updates

import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.serialization.json.*
import okhttp3.*
import okhttp3.HttpUrl.Companion.toHttpUrl
import java.io.File
import java.io.IOException
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import java.security.MessageDigest
import java.util.concurrent.TimeUnit
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

open class UpdateException(message: String) : IOException(message)
/** The update service could not be reached or answered with an error; another source may still work. */
class UpdateUnavailableException(message: String) : UpdateException(message)
/** [sourceTree] is the mobile-native/ git tree a release build came from, or empty. */
data class InstalledVersion(val name: String, val code: Long, val sourceTree: String = "") {
    val comparisonCode: Long get() = maxOf(code, releaseVersionCode("v$name") ?: code)
}
/** [fallbackUrls] are the same APK on the other update sources; the SHA-256 and signer checks still apply to them. */
data class AppUpdate(val tag: String, val notes: String, val url: HttpUrl, val size: Long, val sha256: String, val fallbackUrls: List<HttpUrl> = emptyList()) {
    val version: String get() = tag.removePrefix("v")
    val code: Long get() = requireNotNull(releaseVersionCode(tag))
}

/**
 * One place releases are published. [checksumInApi] is true when the release API itself carries each asset's
 * size and SHA-256 (GitHub); otherwise (Gitee) they come from release.json and a HEAD request on the asset.
 */
internal class UpdateSource(val latestUrl: HttpUrl, val downloadBase: HttpUrl, val checksumInApi: Boolean)

/** Same ordering and Android bounds as scripts/release.py; never compare version strings lexically. */
internal fun releaseVersionCode(tag: String): Long? {
    val match = Regex("v(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(?:-(alpha|beta|rc)\\.([1-9][0-9]*))?").matchEntire(tag) ?: return null
    val major = match.groupValues[1].toLongOrNull()?.takeIf { it <= 210 } ?: return null
    val minor = match.groupValues[2].toLongOrNull()?.takeIf { it <= 99 } ?: return null
    val patch = match.groupValues[3].toLongOrNull()?.takeIf { it <= 99 } ?: return null
    val phase = when (match.groupValues[4]) { "alpha" -> 0; "beta" -> 1; "rc" -> 2; else -> 3 }
    val count = if (phase == 3) 0L else match.groupValues[5].toLongOrNull()?.takeIf { it <= 99 } ?: return null
    return (major * 10_000_000 + minor * 100_000 + patch * 1000 + phase * 100 + count).takeIf { it <= 2_100_000_000 }
}

class UpdateClient internal constructor(
    private val http: OkHttpClient,
    private val sources: List<UpdateSource>,
) {
    internal constructor(http: OkHttpClient, latestUrl: HttpUrl, downloadBase: HttpUrl) :
        this(http, listOf(UpdateSource(latestUrl, downloadBase, checksumInApi = true)))

    /** GitHub is the source of truth; the Gitee mirror is tried when GitHub cannot be reached (mainland China). */
    constructor() : this(
        OkHttpClient.Builder().connectTimeout(15, TimeUnit.SECONDS).readTimeout(30, TimeUnit.SECONDS)
            .followSslRedirects(false)
            .addNetworkInterceptor { chain ->
                val url = chain.request().url
                if (!url.isHttps || url.host !in TRUSTED_HOSTS) {
                    throw UpdateException("更新下载地址不受信任")
                }
                chain.proceed(chain.request())
            }.build(),
        listOf(
            UpdateSource(
                "https://api.github.com/repos/nightgopher/easyagent/releases/latest".toHttpUrl(),
                "https://github.com/nightgopher/easyagent/releases/download/".toHttpUrl(), checksumInApi = true),
            UpdateSource(
                "https://gitee.com/api/v5/repos/weijian-huang/easyagent/releases/latest".toHttpUrl(),
                "https://gitee.com/weijian-huang/easyagent/releases/download/".toHttpUrl(), checksumInApi = false),
        ),
    )

    suspend fun latest(installed: InstalledVersion): AppUpdate? {
        var unreachable: UpdateUnavailableException? = null
        for (source in sources) {
            // Only transport failures move on to the next source; invalid or inconsistent metadata is rejected outright.
            try { return latestFrom(source, installed) } catch (e: UpdateUnavailableException) { unreachable = unreachable ?: e }
        }
        throw unreachable ?: UpdateException("没有可用的更新源")
    }

    private suspend fun latestFrom(source: UpdateSource, installed: InstalledVersion): AppUpdate? {
        val release = json(source.latestUrl)
        val tag = release.text("tag_name")
        val code = releaseVersionCode(tag) ?: throw UpdateException("新版本信息无效")
        fun flag(key: String) = release[key]?.jsonPrimitive?.booleanOrNull
        // Gitee's release API has no "draft" field, so only an explicit true is rejected there.
        val stable = if (source.checksumInApi) flag("draft") == false && flag("prerelease") == false
            else flag("draft") != true && flag("prerelease") != true
        if (!stable || '-' in tag) throw UpdateException("更新通道未提供正式版本")
        if (code <= installed.comparisonCode) return null
        val asset = release["assets"]?.jsonArray?.map { it.jsonObject }?.singleOrNull { it.text("name") == APK }
            ?: throw UpdateException("新版本尚未提供 Android 安装包")
        val expectedUrl = source.downloadBase.resolve("$tag/$APK")!!
        var size = asset["size"]?.jsonPrimitive?.longOrNull ?: 0
        var hash = asset.text("digest").removePrefix("sha256:")
        val urlMatches = asset.text("browser_download_url") == expectedUrl.toString()
        if (source.checksumInApi) {
            if (asset.text("state") != "uploaded" || size !in 1..MAX_APK || !HASH.matches(hash) ||
                asset.text("digest") != "sha256:$hash" || !urlMatches) {
                throw UpdateException("安装包信息或下载地址无效")
            }
        } else if (!urlMatches) {
            throw UpdateException("安装包信息或下载地址无效")
        }
        val manifest = json(source.downloadBase.resolve("$tag/release.json")!!)
        val listed = manifest["assets"]?.jsonObject?.get(APK)?.jsonPrimitive?.contentOrNull
        if (!source.checksumInApi) {
            hash = listed.orEmpty()
            size = contentLength(expectedUrl)
            if (!HASH.matches(hash) || size !in 1..MAX_APK) throw UpdateException("安装包信息或下载地址无效")
        }
        if (manifest.text("version") != tag || !Regex("[0-9a-f]{40}").matches(manifest.text("commit")) ||
            listed != hash || manifest["core_only"]?.jsonPrimitive?.booleanOrNull == true) {
            throw UpdateException("发布清单与安装包校验信息不一致")
        }
        // A release that left mobile-native/ untouched ships this same app under a new version.
        val androidTree = manifest["sources"]?.jsonObject?.get("android")?.jsonPrimitive?.contentOrNull
        if (installed.sourceTree.isNotEmpty() && androidTree == installed.sourceTree) return null
        val fallbackUrls = sources.filter { it !== source }.map { it.downloadBase.resolve("$tag/$APK")!! }
        return AppUpdate(tag, release.text("body").take(32_000), expectedUrl, size, hash, fallbackUrls)
    }

    /** Tries the update's own URL first, then the same APK on the other sources; the hash and signer checks decide validity. */
    suspend fun download(update: AppUpdate, directory: File, progress: (Int) -> Unit, verify: (File, AppUpdate) -> Unit): File {
        var unreachable: UpdateUnavailableException? = null
        for (url in listOf(update.url) + update.fallbackUrls) {
            try { return downloadFrom(url, update, directory, progress, verify) } catch (e: UpdateUnavailableException) { unreachable = unreachable ?: e }
        }
        throw unreachable ?: UpdateException("没有可用的下载地址")
    }

    private suspend fun downloadFrom(url: HttpUrl, update: AppUpdate, directory: File, progress: (Int) -> Unit, verify: (File, AppUpdate) -> Unit): File {
        check(directory.isDirectory || directory.mkdirs()) { "无法创建更新目录" }
        val destination = File(directory, "${update.tag}.apk")
        return request(url, 15 * 60L) { response, active ->
            val partial = File.createTempFile("download-", ".part", directory)
            try {
                val body = response.body ?: throw UpdateException("安装包为空")
                if (body.contentLength() != -1L && body.contentLength() != update.size) throw UpdateException("安装包大小不匹配，请重试")
                val digest = MessageDigest.getInstance("SHA-256")
                var downloaded = 0L
                var percent = -1
                body.byteStream().use { input ->
                    partial.outputStream().use { output ->
                        val buffer = ByteArray(64 * 1024)
                        while (true) {
                            if (!active()) throw UpdateException("下载已取消")
                            val count = input.read(buffer)
                            if (count < 0) break
                            if (!active()) throw UpdateException("下载已取消")
                            downloaded += count
                            if (downloaded > update.size || downloaded > MAX_APK) throw UpdateException("安装包大小不匹配，请重试")
                            output.write(buffer, 0, count)
                            digest.update(buffer, 0, count)
                            val next = (downloaded * 100 / update.size).toInt()
                            if (next != percent) { percent = next; progress(next) }
                        }
                    }
                }
                if (downloaded != update.size || digest.digest().hex() != update.sha256) throw UpdateException("安装包校验失败，请重新下载")
                verify(partial, update)
                if (!active()) throw UpdateException("下载已取消")
                Files.move(partial.toPath(), destination.toPath(), StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
                destination
            } finally { partial.delete() }
        }
    }

    private suspend fun json(url: HttpUrl): JsonObject = request(url, 30) { response, _ ->
        val body = response.body ?: throw UpdateException("更新服务响应为空")
        if (body.contentLength() > MAX_JSON) throw UpdateException("更新信息过大")
        val bytes = body.byteStream().use { input ->
            val output = java.io.ByteArrayOutputStream()
            val buffer = ByteArray(8192)
            while (true) {
                val size = input.read(buffer)
                if (size < 0) break
                if (output.size() + size > MAX_JSON) throw UpdateException("更新信息过大")
                output.write(buffer, 0, size)
            }
            output.toByteArray()
        }
        try { Json.parseToJsonElement(String(bytes, Charsets.UTF_8)).jsonObject }
        catch (_: Exception) { throw UpdateException("更新信息格式无效") }
    }

    /** The asset size the server reports, following redirects (Gitee serves attachments from a different host). */
    private suspend fun contentLength(url: HttpUrl): Long = request(url, 30, "HEAD") { response, _ ->
        response.header("Content-Length")?.toLongOrNull() ?: -1L
    }

    private suspend fun <T> request(url: HttpUrl, seconds: Long, method: String = "GET", read: (Response, () -> Boolean) -> T): T = suspendCancellableCoroutine { continuation ->
        // A separate client with no runtime-host credentials: release traffic never carries a host token.
        val call = http.newCall(Request.Builder().url(url).method(method, null).header("User-Agent", "EasyAgent-Android-Updater").build())
        call.timeout().timeout(seconds, TimeUnit.SECONDS)
        continuation.invokeOnCancellation { call.cancel() }
        call.enqueue(object : Callback {
            override fun onFailure(call: Call, e: IOException) {
                if (continuation.isActive) continuation.resumeWithException(UpdateUnavailableException("无法连接更新服务，请检查网络后重试"))
            }
            override fun onResponse(call: Call, response: Response) {
                try {
                    val result = response.use {
                        if (!it.isSuccessful) throw UpdateUnavailableException(if (it.code == 403 || it.code == 429) "更新检查过于频繁，请稍后重试" else "更新服务暂不可用（HTTP ${it.code}）")
                        read(it) { continuation.isActive }
                    }
                    if (continuation.isActive) continuation.resume(result)
                } catch (e: Exception) {
                    // An I/O error while reading is a dropped connection, so another source is worth trying.
                    if (continuation.isActive) continuation.resumeWithException(when (e) {
                        is UpdateException -> e
                        is IOException -> UpdateUnavailableException("更新读取中断，请检查网络后重试")
                        else -> UpdateException("更新读取失败，请重试")
                    })
                }
            }
        })
    }

    companion object {
        private val TRUSTED_HOSTS = setOf(
            "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com",
            "gitee.com", "foruda.gitee.com",
        )
        private const val APK = "easyagent-android.apk"
        private const val MAX_APK = 200L * 1024 * 1024
        private const val MAX_JSON = 2 * 1024 * 1024
        private val HASH = Regex("[0-9a-f]{64}")
    }
}

private fun JsonObject.text(key: String): String = this[key]?.jsonPrimitive?.contentOrNull.orEmpty()
internal fun ByteArray.hex(): String = joinToString("") { "%02x".format(it) }

internal fun verifyDownloadedFile(file: File, update: AppUpdate) {
    if (!file.isFile || file.length() != update.size) throw UpdateException("安装包已丢失或损坏，请重新下载")
    val digest = MessageDigest.getInstance("SHA-256")
    file.inputStream().use { input ->
        val buffer = ByteArray(64 * 1024)
        while (true) {
            val n = input.read(buffer)
            if (n < 0) break
            digest.update(buffer, 0, n)
        }
    }
    if (digest.digest().hex() != update.sha256) throw UpdateException("安装包校验失败，请重新下载")
}
