package com.hwj.easyagent.mobile.backend

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

/** 每台主机持有独立连接；切换只改变选择，不销毁其他主机的会话、草稿和重连任务。 */
class HostConnections(
    private val settings: SettingsStore,
    private val onAlert: ((AgentClient, Alert) -> Unit)? = null,
) {
    private val clients = LinkedHashMap<String, AgentClient>()
    // Coil resolves image transports on worker threads. Publish a complete,
    // immutable snapshot instead of iterating the UI-owned mutable map there.
    @Volatile private var imageClients: List<AgentClient> = emptyList()
    private val mutableState = MutableStateFlow(HostSettings())
    private val mutableClient = MutableStateFlow<AgentClient?>(null)
    val state = mutableState.asStateFlow()
    val client = mutableClient.asStateFlow()

    init {
        update(settings.load())
    }

    fun clientFor(id: String): AgentClient? = clients[id]

    fun save(config: HostConfig) {
        val existing = if (config.relay != null) state.value.hosts.firstOrNull { it.url == config.url && it.relay != null } else null
        settings.save(if (existing != null) config.copy(id = existing.id) else config)
        update(settings.load())
    }

    fun select(id: String) {
        settings.select(id)
        update(mutableState.value.copy(activeId = id))
        clients[id]?.resume()
    }

    fun remove(id: String) {
        settings.remove(id)
        update(settings.load())
    }

    fun resume() {
        clients.values.forEach { it.resume() }
    }

    fun shutdown() {
        imageClients = emptyList()
        clients.values.forEach { it.shutdown() }
        clients.clear()
        mutableClient.value = null
    }

    private fun update(next: HostSettings) {
        val iterator = clients.iterator()
        while (iterator.hasNext()) {
            val (id, client) = iterator.next()
            val host = next.hosts.firstOrNull { it.id == id }
            if (host == null || host.url != client.config.url || host.token != client.config.token || host.relay != client.config.relay) {
                client.shutdown()
                iterator.remove()
            }
        }
        for (host in next.hosts) {
            // 解密失败的配置仍可编辑/删除，但绝不降级为无令牌请求。
            if (host.token != null && !clients.containsKey(host.id)) {
                clients[host.id] = AgentClient(host, settings).also {
                    it.onAlert = onAlert
                    it.start()
                }
            }
        }
        imageClients = next.hosts.mapNotNull { clients[it.id] }
        mutableState.value = next
        mutableClient.value = clients[next.activeId]
    }

    /** Bearer token of the saved host matching [url]'s origin, for the shared image loader. */
    fun tokenFor(url: String): String? {
        val origin = originOf(url) ?: return null
        return state.value.hosts.firstOrNull { originOf(it.url) == origin }?.token?.takeIf { it.isNotEmpty() }
    }

    fun httpFor(url: String): okhttp3.OkHttpClient? {
        val origin = originOf(url) ?: return null
        return imageClients.firstOrNull { originOf(it.config.url) == origin }?.api?.http
    }

    private fun originOf(url: String): String? = url.toHttpUrlOrNull()?.let { "${it.scheme}://${it.host}:${it.port}" }
}
