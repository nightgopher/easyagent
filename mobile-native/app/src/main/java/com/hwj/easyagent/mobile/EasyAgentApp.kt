package com.hwj.easyagent.mobile

import android.app.Application
import android.content.Intent
import kotlinx.coroutines.*
import com.hwj.easyagent.mobile.backend.HostConnections
import com.hwj.easyagent.mobile.backend.SettingsStore
import kotlinx.coroutines.flow.MutableStateFlow
import com.hwj.easyagent.mobile.updates.AppUpdater
import com.hwj.easyagent.mobile.updates.createAppUpdater

/** A host and session to open, e.g. after tapping a notification. */
data class OpenRequest(val host: String, val session: String)

class EasyAgentApp : Application() {
    lateinit var connections: HostConnections
        private set
    internal lateinit var updater: AppUpdater

    val shareError = MutableStateFlow<String?>(null)
    val shares = MutableStateFlow<List<com.hwj.easyagent.mobile.media.SharedCapture>>(emptyList())

    /** Set by MainActivity from a notification's intent; consumed by the UI. */
    val pendingOpen = MutableStateFlow<OpenRequest?>(null)

    private val shareScope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    fun acceptShare(intent: Intent) {
        shareScope.launch {
            try {
                com.hwj.easyagent.mobile.media.receiveShare(this@EasyAgentApp, intent)
                shares.value = withContext(Dispatchers.IO) { com.hwj.easyagent.mobile.media.loadShares(this@EasyAgentApp) }
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                shareError.value = "无法接收分享：${e.message}"
            }
        }
    }

    override fun onCreate() {
        super.onCreate()
        shares.value = com.hwj.easyagent.mobile.media.loadShares(this)
        updater = createAppUpdater(this)
        val notifier = Notifier(this)
        connections = HostConnections(SettingsStore(this)) { client, alert -> notifier.post(client, alert) }
    }
}
