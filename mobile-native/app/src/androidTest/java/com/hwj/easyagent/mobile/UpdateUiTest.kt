package com.hwj.easyagent.mobile

import android.accessibilityservice.AccessibilityService
import android.graphics.Bitmap
import android.os.Build
import android.os.ParcelFileDescriptor
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.hwj.easyagent.mobile.updates.*
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.json.JSONArray
import org.json.JSONObject
import org.junit.*
import org.junit.Assert.*
import org.junit.runner.RunWith
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.atomic.AtomicInteger

/** The real Activity downloads a real same-certificate APK and opens Android's installer. */
@RunWith(AndroidJUnit4::class)
class UpdateUiTest {
    @get:Rule val ui = createEmptyComposeRule()
    private val instrumentation = InstrumentationRegistry.getInstrumentation()
    private val app get() = instrumentation.targetContext.applicationContext as EasyAgentApp
    private lateinit var activity: ActivityScenario<MainActivity>
    private lateinit var server: MockWebServer
    private lateinit var bytes: ByteArray
    private val failures = AtomicInteger()
    private var corrupt = false
    private var packageMismatch = false
    private lateinit var previous: AppUpdater
    private val directory get() = File(app.filesDir, "updates")
    private val calls = mutableListOf<String?>()

    @Before fun prepare() {
        bytes = instrumentation.context.assets.open("update.apk").use { it.readBytes() }
        directory.deleteRecursively()
        server = MockWebServer()
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                synchronized(calls) { calls += request.getHeader("Authorization") }
                if (failures.getAndUpdate { maxOf(0, it - 1) } > 0) return MockResponse().setResponseCode(503)
                val payload = if (packageMismatch) File(instrumentation.context.applicationInfo.sourceDir).readBytes() else bytes
                val digest = MessageDigest.getInstance("SHA-256").digest(payload).hex()
                return when (request.path) {
                    "/latest" -> MockResponse().setBody(JSONObject().put("tag_name", "v99.0.0").put("draft", false).put("prerelease", false)
                        .put("body", "Android 更新验收：保留主机与凭据。")
                        .put("assets", JSONArray().put(JSONObject().put("name", "easyagent-android.apk").put("state", "uploaded")
                            .put("size", payload.size).put("digest", "sha256:$digest")
                            .put("browser_download_url", server.url("/download/v99.0.0/easyagent-android.apk").toString()))).toString())
                    "/download/v99.0.0/release.json" -> MockResponse().setBody(JSONObject().put("version", "v99.0.0").put("commit", "a".repeat(40))
                        .put("assets", JSONObject().put("easyagent-android.apk", digest)).toString())
                    "/download/v99.0.0/easyagent-android.apk" -> MockResponse().setBody(okio.Buffer().write(if (corrupt) payload.copyOfRange(0, 100) else payload))
                    else -> MockResponse().setResponseCode(404)
                }
            }
        }
        server.start()
        val client = UpdateClient(OkHttpClient(), server.url("/latest"), server.url("/download/"))
        instrumentation.runOnMainSync {
            app.connections.state.value.hosts.map { it.id }.forEach(app.connections::remove)
            previous = app.updater
            app.updater = AppUpdater(
                previous.installed, client, directory,
                object : UpdatePreferences {
                    override var automatic = false
                    override var lastAttempt = 0L
                    override var dismissedTag = ""
                }, { file, update -> verifyAndroidApk(app, file, update) },
            )
        }
    }

    @After fun close() {
        if (::activity.isInitialized) activity.close()
        instrumentation.runOnMainSync { app.updater.close(); app.updater = previous }
        server.close()
        directory.deleteRecursively()
        // AGP uninstalls the test app. Revoking this app-op here kills the instrumented process.
    }

    private fun shell(command: String): String = ParcelFileDescriptor.AutoCloseInputStream(instrumentation.uiAutomation.executeShellCommand(command)).bufferedReader().use { it.readText() }
    private fun waitText(text: String) = ui.waitUntil(20_000) { ui.onAllNodesWithText(text, substring = true).fetchSemanticsNodes().isNotEmpty() }
    private fun launch() { activity = ActivityScenario.launch(MainActivity::class.java) }
    private fun check() {
        ui.onNodeWithText("检查更新").performClick()
        waitText("发现新版本 99.0.0")
    }
    private fun download() {
        ui.onNodeWithText("下载更新").performClick()
        waitText("安装更新")
    }
    private fun back() { instrumentation.uiAutomation.performGlobalAction(AccessibilityService.GLOBAL_ACTION_BACK) }
    private fun waitPackage(predicate: (String) -> Boolean) {
        ui.waitUntil(15_000) { predicate(instrumentation.uiAutomation.rootInActiveWindow?.packageName?.toString().orEmpty()) }
    }
    private fun screenshot(name: String) {
        if (Build.VERSION.SDK_INT >= 31) {
            val bitmap = instrumentation.uiAutomation.takeScreenshot()
            shell("mkdir -p /data/local/tmp/easyagent-ui")
            val pipes = instrumentation.uiAutomation.executeShellCommandRw("dd of=/data/local/tmp/easyagent-ui/$name.png")
            ParcelFileDescriptor.AutoCloseOutputStream(pipes[1]).use { requireNotNull(bitmap).compress(Bitmap.CompressFormat.PNG, 100, it) }
            ParcelFileDescriptor.AutoCloseInputStream(pipes[0]).use { it.readBytes() }
            bitmap?.recycle()
        }
    }

    @Test fun automaticCheckPromptsOnceAndManualEntryWorksWithoutAnyHost() {
        instrumentation.runOnMainSync { app.updater.setAutomatic(true) }
        launch()
        waitText("发现新版本 99.0.0")
        ui.onNodeWithText("稍后").performClick()
        waitText("连接运行主机")
        activity.recreate()
        waitText("连接运行主机")
        assertEquals(2, server.requestCount)
        check()
        assertEquals(4, server.requestCount)
        synchronized(calls) { assertTrue(calls.all { it == null }) }
    }

    @Test fun networkAndCorruptDownloadsShowRecoverableErrors() {
        failures.set(1)
        launch()
        ui.onNodeWithText("检查更新").performClick()
        waitText("HTTP 503")
        ui.onAllNodesWithText("检查更新").onLast().performClick()
        waitText("发现新版本 99.0.0")
        corrupt = true
        ui.onNodeWithText("下载更新").performClick()
        waitText("安装包大小不匹配")
        ui.onAllNodesWithText("安装更新").assertCountEquals(0)
        corrupt = false
        ui.onNodeWithText("重新下载").performClick()
        waitText("安装更新")
    }

    @Test fun foreignApkIsRejectedAfterItsChecksumPasses() {
        packageMismatch = true
        launch()
        check()
        ui.onNodeWithText("下载更新").performClick()
        waitText("安装包的应用或版本不匹配")
        ui.onAllNodesWithText("安装更新").assertCountEquals(0)
        assertTrue(directory.listFiles().orEmpty().none { it.extension == "apk" })
    }

    @Test fun wrongSigningCertificateCannotReplaceTheApp() {
        bytes = instrumentation.context.assets.open("untrusted.apk").use { it.readBytes() }
        launch()
        check()
        ui.onNodeWithText("下载更新").performClick()
        waitText("安装包签名与当前应用不一致")
        ui.onAllNodesWithText("安装更新").assertCountEquals(0)
        assertTrue(directory.listFiles().orEmpty().none { it.extension == "apk" })
    }

    @Test fun permissionDenialKeepsDownloadAndGrantOpensTheSystemInstaller() {
        assertFalse(app.packageManager.canRequestPackageInstalls())
        launch()
        check()
        download()
        screenshot("update-ready")
        val downloaded = requireNotNull(app.updater.state.value.file)
        val intent = updateInstallIntent(app, downloaded)
        assertEquals("content", intent.data!!.scheme)
        assertArrayEquals(bytes, app.contentResolver.openInputStream(intent.data!!)!!.use { it.readBytes() })
        ui.onNodeWithText("安装更新").performClick()
        waitPackage { it == "com.android.settings" }
        back()
        waitText("尚未允许安装更新")
        assertTrue(downloaded.isFile)
        ui.onNodeWithText("安装更新").performClick()
        waitPackage { it == "com.android.settings" }
        shell("appops set ${app.packageName} REQUEST_INSTALL_PACKAGES allow")
        back()
        waitPackage { it.contains("packageinstaller") }
        try {
            ui.waitUntil(15_000) {
                val root = instrumentation.uiAutomation.rootInActiveWindow
                root?.findAccessibilityNodeInfosByText("EasyAgent")?.isNotEmpty() == true &&
                    root.findAccessibilityNodeInfosByText("Update").any { it.text?.toString().equals("Update", ignoreCase = true) }
            }
        } catch (e: Exception) { screenshot("update-installer-failure"); throw e }
        screenshot("update-installer")
        back() // Do not replace the instrumented process; full replacement is checked separately.
        waitText("安装更新")
        assertTrue(downloaded.isFile)
        assertEquals(3, server.requestCount)
    }
}
