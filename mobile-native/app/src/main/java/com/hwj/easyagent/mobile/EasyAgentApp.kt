package com.hwj.easyagent.mobile

import android.app.Application
import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.HostConfig
import com.hwj.easyagent.mobile.backend.SettingsStore
import kotlinx.coroutines.flow.MutableStateFlow

class EasyAgentApp : Application() {
    lateinit var settings: SettingsStore
        private set

    /** 当前运行主机连接；为空表示尚未配置。 */
    val client = MutableStateFlow<AgentClient?>(null)

    override fun onCreate() {
        super.onCreate()
        settings = SettingsStore(this)
        settings.load()?.let { if (it.url.isNotEmpty()) connect(it, persist = false) }
    }

    fun connect(config: HostConfig, persist: Boolean = true) {
        if (persist) settings.save(config)
        client.value?.shutdown()
        client.value = AgentClient(config).also { it.start() }
    }

    fun disconnect() {
        client.value?.shutdown()
        client.value = null
        settings.clear()
    }
}
