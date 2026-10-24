package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.updates.*
import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.security.MessageDigest

class UpdateClientTest {
    @get:Rule val folder = TemporaryFolder()
    private val apk = "test APK bytes".toByteArray()
    private val hash = MessageDigest.getInstance("SHA-256").digest(apk).joinToString("") { "%02x".format(it) }
    private val installed = InstalledVersion("0.24.0", 2400300)

    private fun release(server: MockWebServer, tag: String = "v0.25.0", hash: String = this.hash) = """
        {"tag_name":"$tag","draft":false,"prerelease":false,"body":"更新说明",
         "assets":[{"name":"easyagent-android.apk","state":"uploaded","size":${apk.size},
         "digest":"sha256:$hash","browser_download_url":"${server.url("/download/$tag/easyagent-android.apk")}"}]}
    """.trimIndent()

    private fun client(server: MockWebServer) = UpdateClient(OkHttpClient(), server.url("/latest"), server.url("/download/"))
    private fun manifest(hash: String = this.hash) = """{"version":"v0.25.0","commit":"${"a".repeat(40)}","assets":{"easyagent-android.apk":"$hash"}}"""

    @Test fun `version comparison follows release versionCode and never downgrades`() = runBlocking {
        assertEquals(2500300L, releaseVersionCode("v0.25.0"))
        assertEquals(2500201L, releaseVersionCode("v0.25.0-rc.1"))
        for (invalid in listOf("v00.25.0", "v0.100.0", "v0.25.0-rc.0", "v999999999999999999999.0.0")) {
            assertNull(releaseVersionCode(invalid))
        }
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody(release(server, "v0.24.0")))
            assertNull(client(server).latest(installed))
            server.enqueue(MockResponse().setBody(release(server, "v0.24.0")))
            // A development APK's versionCode is 1, but its versionName still prevents downgrades.
            assertNull(client(server).latest(InstalledVersion("0.25.0", 1)))
            assertEquals(2, server.requestCount)
        }
    }

    @Test fun `a release that left the Android sources untouched is not offered`() = runBlocking {
        val tree = "b".repeat(40)
        val withSources = manifest().removeSuffix("}") + ""","sources":{"android":"$tree"}}"""
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody(release(server)))
            server.enqueue(MockResponse().setBody(withSources))
            assertNull(client(server).latest(installed.copy(sourceTree = tree)))
            // Different sources, or a development build without a tree, still update.
            server.enqueue(MockResponse().setBody(release(server)))
            server.enqueue(MockResponse().setBody(withSources))
            assertNotNull(client(server).latest(installed.copy(sourceTree = "c".repeat(40))))
            server.enqueue(MockResponse().setBody(release(server)))
            server.enqueue(MockResponse().setBody(withSources))
            assertNotNull(client(server).latest(installed))
        }
    }

    @Test fun `only complete stable release and matching checksum can be downloaded`() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody(release(server)))
            server.enqueue(MockResponse().setBody(manifest()))
            server.enqueue(MockResponse().setBody(okio.Buffer().write(apk)))
            val client = client(server)
            val update = requireNotNull(client.latest(installed))
            var verified = false
            val progress = mutableListOf<Int>()
            val file = client.download(update, folder.root, { progress += it }) { bytes, info ->
                assertArrayEquals(apk, bytes.readBytes())
                assertEquals("v0.25.0", info.tag)
                verified = true
            }
            assertTrue(verified)
            assertArrayEquals(apk, file.readBytes())
            assertEquals(100, progress.last())
            repeat(3) { assertNull(server.takeRequest().getHeader("Authorization")) }
            assertEquals(listOf(file), folder.root.listFiles()!!.toList())
        }
    }

    @Test fun `rejects malformed metadata foreign links prereleases and inconsistent digests`() = runBlocking {
        MockWebServer().use { server ->
            val bad = listOf(
                release(server).replace("\"draft\":false", "\"draft\":true"),
                release(server).replace("\"prerelease\":false", "\"prerelease\":true"),
                release(server).replace(server.url("/download/").toString(), "https://untrusted.example/"),
                release(server).replace("\"size\":${apk.size}", "\"size\":0"),
                release(server).replace("sha256:$hash", "sha256:invalid"),
            )
            for (json in bad) {
                server.enqueue(MockResponse().setBody(json))
                try { client(server).latest(installed); fail("accepted invalid release") } catch (_: UpdateException) { }
            }
            server.enqueue(MockResponse().setBody(release(server)))
            server.enqueue(MockResponse().setBody(manifest("b".repeat(64))))
            try { client(server).latest(installed); fail("accepted inconsistent digest") } catch (_: UpdateException) { }
        }
    }

    @Test fun `corrupt truncated and wrong signer downloads never become installable`() = runBlocking {
        MockWebServer().use { server ->
            val client = client(server)
            server.enqueue(MockResponse().setBody(release(server)))
            server.enqueue(MockResponse().setBody(manifest()))
            val update = requireNotNull(client.latest(installed))
            for (body in listOf("corrupt bytes", "short", String(apk))) {
                server.enqueue(MockResponse().setBody(body))
                try {
                    client.download(update, folder.root, {}) { _, _ -> throw UpdateException("签名不匹配") }
                    fail("accepted invalid APK")
                } catch (_: UpdateException) { }
                assertTrue(folder.root.listFiles()!!.isEmpty())
            }
        }
    }

    // Gitee's release API carries neither digest nor size, so those come from release.json and a HEAD request.
    private fun mirrorRelease(server: MockWebServer, tag: String = "v0.25.0") = """
        {"tag_name":"$tag","prerelease":false,"body":"镜像说明",
         "assets":[{"name":"easyagent-android.apk","browser_download_url":"${server.url("/mirror/download/$tag/easyagent-android.apk")}"}]}
    """.trimIndent()

    private fun clientWithMirror(server: MockWebServer) = UpdateClient(OkHttpClient(), listOf(
        UpdateSource(server.url("/latest"), server.url("/download/"), checksumInApi = true),
        UpdateSource(server.url("/mirror/latest"), server.url("/mirror/download/"), checksumInApi = false),
    ))

    private fun MockWebServer.enqueueMirrorMetadata(manifest: String = manifest()) {
        enqueue(MockResponse().setBody(mirrorRelease(this)))
        enqueue(MockResponse().setBody(manifest))
        enqueue(MockResponse().addHeader("Content-Length", apk.size))
    }

    @Test fun `uses the mirror when the primary source cannot be reached`() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setResponseCode(503))
            server.enqueueMirrorMetadata()
            server.enqueue(MockResponse().setBody(okio.Buffer().write(apk)))
            val client = clientWithMirror(server)
            val update = requireNotNull(client.latest(installed))
            assertEquals(server.url("/mirror/download/v0.25.0/easyagent-android.apk"), update.url)
            assertEquals(apk.size.toLong(), update.size)
            assertEquals(hash, update.sha256)
            assertEquals("镜像说明", update.notes)
            val file = client.download(update, folder.root, {}) { _, _ -> }
            assertArrayEquals(apk, file.readBytes())
            val requests = List(5) { server.takeRequest() }
            assertEquals(listOf("GET /latest", "GET /mirror/latest", "GET /mirror/download/v0.25.0/release.json",
                "HEAD /mirror/download/v0.25.0/easyagent-android.apk", "GET /mirror/download/v0.25.0/easyagent-android.apk"),
                requests.map { "${it.method} ${it.path}" })
        }
    }

    @Test fun `invalid metadata from the primary source is rejected instead of trying the mirror`() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody(release(server).replace("\"draft\":false", "\"draft\":true")))
            try { clientWithMirror(server).latest(installed); fail("accepted a draft release") }
            catch (e: UpdateException) { assertFalse(e is UpdateUnavailableException) }
            assertEquals(1, server.requestCount)
        }
    }

    @Test fun `download moves to the same APK on the other source when the first one fails`() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody(release(server)))
            server.enqueue(MockResponse().setBody(manifest()))
            val client = clientWithMirror(server)
            val update = requireNotNull(client.latest(installed))
            assertEquals(listOf(server.url("/mirror/download/v0.25.0/easyagent-android.apk")), update.fallbackUrls)
            server.enqueue(MockResponse().setResponseCode(502))
            server.enqueue(MockResponse().setBody(okio.Buffer().write(apk)))
            val file = client.download(update, folder.root, {}) { _, _ -> }
            assertArrayEquals(apk, file.readBytes())
            server.takeRequest(); server.takeRequest()
            assertEquals("/download/v0.25.0/easyagent-android.apk", server.takeRequest().path)
            assertEquals("/mirror/download/v0.25.0/easyagent-android.apk", server.takeRequest().path)
        }
    }

    @Test fun `a mirror that serves different bytes than its manifest lists is rejected`() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setResponseCode(503))
            server.enqueueMirrorMetadata()
            val client = clientWithMirror(server)
            val update = requireNotNull(client.latest(installed))
            server.enqueue(MockResponse().setBody("tampered APK!".padEnd(apk.size, '!')))
            try { client.download(update, folder.root, {}) { _, _ -> }; fail("accepted tampered bytes") }
            catch (e: UpdateException) { assertFalse(e is UpdateUnavailableException) }
            assertTrue(folder.root.listFiles()!!.isEmpty())
        }
    }
}
