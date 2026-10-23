package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.updates.*
import kotlinx.coroutines.*
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.util.concurrent.TimeUnit

class AppUpdaterTest {
    @get:Rule val folder = TemporaryFolder()
    private class Prefs : UpdatePreferences {
        override var automatic = true
        override var lastAttempt = 0L
        override var dismissedTag = ""
    }
    private val bytes = ByteArray(64 * 1024) { (it % 127).toByte() }
    private val hash get() = java.security.MessageDigest.getInstance("SHA-256").digest(bytes).hex()
    private var now = 1000L

    private fun enqueue(server: MockWebServer) {
        server.enqueue(MockResponse().setBody("""{"tag_name":"v0.25.0","draft":false,"prerelease":false,"body":"说明","assets":[{"name":"easyagent-android.apk","state":"uploaded","size":${bytes.size},"digest":"sha256:$hash","browser_download_url":"${server.url("/download/v0.25.0/easyagent-android.apk")}"}]}"""))
        server.enqueue(MockResponse().setBody("""{"version":"v0.25.0","commit":"${"a".repeat(40)}","assets":{"easyagent-android.apk":"$hash"}}"""))
    }
    private fun updater(server: MockWebServer, prefs: Prefs) = AppUpdater(
        InstalledVersion("0.24.0", 2400300), UpdateClient(OkHttpClient(), server.url("/latest"), server.url("/download/")),
        folder.root, prefs, { _, _ -> }, CoroutineScope(SupervisorJob() + Dispatchers.Unconfined), { now },
    )
    private suspend fun waitFor(test: () -> Boolean) = withTimeout(5_000) { while (!test()) delay(10) }

    @Test fun `automatic checks are throttled persisted dismissible and manual checks remain available`() = runBlocking {
        MockWebServer().use { server ->
            val prefs = Prefs()
            val updater = updater(server, prefs)
            try {
                enqueue(server)
                updater.foreground()
                waitFor { updater.state.value.checked && !updater.state.value.busy }
                assertTrue(updater.state.value.visible)
                assertEquals(2, server.requestCount)
                updater.dismiss()
                updater.foreground()
                assertFalse(updater.state.value.visible)
                assertEquals(2, server.requestCount)
                now += AppUpdater.CHECK_INTERVAL
                enqueue(server)
                updater.foreground()
                waitFor { server.requestCount == 4 && !updater.state.value.busy }
                assertFalse(updater.state.value.visible)
                updater.setAutomatic(false)
                now += AppUpdater.CHECK_INTERVAL
                updater.foreground()
                assertEquals(4, server.requestCount)
                enqueue(server)
                updater.open()
                waitFor { server.requestCount == 6 && !updater.state.value.busy }
                assertTrue(updater.state.value.visible)
                assertFalse(prefs.automatic)
                assertEquals("v0.25.0", prefs.dismissedTag)
            } finally { updater.close() }
        }
    }

    @Test fun `failed checks do not interrupt startup and manual retry recovers`() = runBlocking {
        MockWebServer().use { server ->
            val updater = updater(server, Prefs())
            try {
                server.enqueue(MockResponse().setResponseCode(503))
                updater.foreground()
                waitFor { updater.state.value.error != null && !updater.state.value.busy }
                assertFalse(updater.state.value.visible)
                enqueue(server)
                updater.open()
                waitFor { updater.state.value.update != null && !updater.state.value.busy }
                assertNull(updater.state.value.error)
                assertTrue(updater.state.value.visible)
            } finally { updater.close() }
        }
    }

    @Test fun `closing a pending check does not reopen the dialog on completion`() = runBlocking {
        MockWebServer().use { server ->
            val updater = updater(server, Prefs())
            try {
                enqueue(server)
                updater.open()
                updater.dismiss()
                waitFor { updater.state.value.update != null && !updater.state.value.busy }
                assertFalse(updater.state.value.visible)
            } finally { updater.close() }
        }
    }

    @Test fun `cancelled partial download can be retried and file is checked again before installation`() = runBlocking {
        MockWebServer().use { server ->
            val updater = updater(server, Prefs())
            try {
                enqueue(server)
                updater.open()
                waitFor { updater.state.value.update != null && !updater.state.value.busy }
                server.enqueue(MockResponse().setBody(okio.Buffer().write(bytes)).throttleBody(1024, 50, TimeUnit.MILLISECONDS))
                updater.download()
                waitFor { (updater.state.value.progress ?: 0) > 0 }
                updater.cancelDownload()
                waitFor { !updater.state.value.busy && folder.root.listFiles()!!.isEmpty() }
                assertNull(updater.state.value.file)
                server.enqueue(MockResponse().setBody(okio.Buffer().write(bytes)))
                updater.download()
                waitFor { updater.state.value.file != null && !updater.state.value.busy }
                var installed = false
                updater.install { installed = true }
                waitFor { !updater.state.value.busy }
                assertTrue(installed)
                updater.state.value.file!!.appendText("tampered after verification")
                installed = false
                updater.install { installed = true }
                waitFor { updater.state.value.error != null && !updater.state.value.busy }
                assertFalse(installed)
                assertNull(updater.state.value.file)
            } finally { updater.close() }
        }
    }

    @Test fun `a new process recovers a verified full download and rejects corrupted cache`() = runBlocking {
        MockWebServer().use { server ->
            val prefs = Prefs()
            val first = updater(server, prefs)
            enqueue(server)
            first.open()
            waitFor { first.state.value.update != null && !first.state.value.busy }
            server.enqueue(MockResponse().setBody(okio.Buffer().write(bytes)))
            first.download()
            waitFor { first.state.value.file != null && !first.state.value.busy }
            first.close()
            val second = updater(server, prefs)
            try {
                enqueue(server)
                second.open()
                waitFor { second.state.value.file != null && !second.state.value.busy }
                assertEquals(5, server.requestCount) // No second APK transfer.
                second.state.value.file!!.writeText("damaged")
                enqueue(server)
                second.check()
                waitFor { server.requestCount == 7 && !second.state.value.busy }
                assertNull(second.state.value.file)
                assertTrue(folder.root.listFiles()!!.isEmpty())
            } finally { second.close() }
        }
    }
}
