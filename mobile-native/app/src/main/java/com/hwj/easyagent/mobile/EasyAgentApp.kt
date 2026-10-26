package com.hwj.easyagent.mobile

import android.app.Application
import coil.ImageLoader
import com.hwj.easyagent.mobile.backend.HostConnections
import com.hwj.easyagent.mobile.backend.SettingsStore
import kotlinx.coroutines.flow.MutableStateFlow
import com.hwj.easyagent.mobile.updates.AppUpdater
import com.hwj.easyagent.mobile.updates.createAppUpdater
import okhttp3.OkHttpClient

/** A host and session to open, e.g. after tapping a notification. */
data class OpenRequest(val host: String, val session: String)

class EasyAgentApp : Application() {
    lateinit var connections: HostConnections
        private set
    lateinit var settings: SettingsStore
        private set
    lateinit var imageLoader: ImageLoader
        private set
    internal lateinit var updater: AppUpdater

    /** Set by MainActivity from a notification's intent; consumed by the UI. */
    val pendingOpen = MutableStateFlow<OpenRequest?>(null)

    override fun onCreate() {
        super.onCreate()
        updater = createAppUpdater(this)
        settings = SettingsStore(this)
        val notifier = Notifier(this)
        connections = HostConnections(settings) { client, alert -> notifier.post(client, alert) }
        // 附件图片的共享加载器：按图片地址匹配已保存主机并注入 Bearer 令牌，自带内存与磁盘缓存。
        val http = OkHttpClient.Builder()
            .addInterceptor { chain ->
                val request = chain.request()
                val token = connections.tokenFor(request.url.toString())
                chain.proceed(
                    if (token.isNullOrEmpty()) request
                    else request.newBuilder().header("Authorization", "Bearer $token").build(),
                )
            }
            .build()
        imageLoader = ImageLoader.Builder(this)
            .okHttpClient(http)
            .crossfade(true)
            .build()
    }
}
