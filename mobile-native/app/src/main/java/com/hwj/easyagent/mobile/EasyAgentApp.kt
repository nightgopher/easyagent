package com.hwj.easyagent.mobile

import android.app.Application
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

    /** Set by MainActivity from a notification's intent; consumed by the UI. */
    val pendingOpen = MutableStateFlow<OpenRequest?>(null)

    override fun onCreate() {
        super.onCreate()
        updater = createAppUpdater(this)
        val notifier = Notifier(this)
        connections = HostConnections(SettingsStore(this)) { client, alert -> notifier.post(client, alert) }
    }
}
