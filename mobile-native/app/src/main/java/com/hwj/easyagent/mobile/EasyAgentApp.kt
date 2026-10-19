package com.hwj.easyagent.mobile

import android.app.Application
import com.hwj.easyagent.mobile.backend.HostConnections
import com.hwj.easyagent.mobile.backend.SettingsStore

class EasyAgentApp : Application() {
    lateinit var connections: HostConnections
        private set

    override fun onCreate() {
        super.onCreate()
        connections = HostConnections(SettingsStore(this))
    }
}
