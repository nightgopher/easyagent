package com.hwj.easyagent.mobile.backend

import android.content.Context
import android.content.SharedPreferences
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import java.util.UUID

data class HostConfig(
    val url: String,
    // null 表示已保存的令牌无法解密；空字符串才表示明确不使用令牌。
    val token: String?,
    val id: String = UUID.randomUUID().toString(),
    val name: String = "",
) {
    val label: String get() = name.ifBlank { url }
}

data class HostSettings(val hosts: List<HostConfig> = emptyList(), val activeId: String? = null)

/** 输入框草稿按主机+会话落盘，进程被系统回收后恢复。 */
interface DraftStore {
    fun load(hostId: String, sessionId: String): String
    fun save(hostId: String, sessionId: String, text: String)
    fun clear(hostId: String, sessionId: String)
}

internal object NoDrafts : DraftStore {
    override fun load(hostId: String, sessionId: String): String = ""
    override fun save(hostId: String, sessionId: String, text: String) {}
    override fun clear(hostId: String, sessionId: String) {}
}

@Serializable
private data class StoredHost(val id: String, val name: String, val url: String)

/** 运行主机地址与令牌。令牌用 Android Keystore 内的 AES 密钥加密后再落盘，对应桌面端的钥匙串。 */
class SettingsStore internal constructor(private val prefs: SharedPreferences, private val cipher: TokenCipher) : DraftStore {
    constructor(context: Context) : this(context.getSharedPreferences("easyagent-host", Context.MODE_PRIVATE), KeystoreTokenCipher())

    fun load(): HostSettings {
        val hosts = records().map {
            val token = prefs.getString(tokenKey(it.id), null)?.let(cipher::decrypt)
            HostConfig(it.url, token, it.id, it.name)
        }
        val selected = prefs.getString(KEY_ACTIVE, null)
        return HostSettings(hosts, selected?.takeIf { id -> hosts.any { it.id == id } } ?: hosts.firstOrNull()?.id)
    }

    fun save(config: HostConfig) {
        val hosts = records().toMutableList()
        val url = ApiClient.normalize(config.url)
        require(hosts.none { it.id != config.id && it.url == url }) { "此地址已保存，请编辑已有主机" }
        val token = requireNotNull(config.token) { "请重新填写此主机的服务令牌" }
        val encrypted = cipher.encrypt(token)
        check(cipher.decrypt(encrypted) == token) { "无法保存服务令牌，请重试" }
        val record = StoredHost(config.id, config.name.trim(), url)
        val index = hosts.indexOfFirst { it.id == config.id }
        if (index < 0) hosts.add(record) else hosts[index] = record
        commit(prefs.edit().putString(KEY_HOSTS, encode(hosts)).putString(tokenKey(config.id), encrypted).putString(KEY_ACTIVE, config.id))
    }

    fun select(id: String) {
        require(records().any { it.id == id }) { "主机连接不存在" }
        commit(prefs.edit().putString(KEY_ACTIVE, id))
    }

    fun remove(id: String) {
        val state = load()
        val hosts = records().filter { it.id != id }
        val selected = state.activeId?.takeIf { it != id } ?: hosts.firstOrNull()?.id
        val editor = prefs.edit().putString(KEY_HOSTS, encode(hosts)).putString(KEY_ACTIVE, selected).remove(tokenKey(id))
        prefs.all.keys.filter { it.startsWith("$id:") || it.startsWith("draft:$id:") }.forEach(editor::remove)
        commit(editor)
    }

    /** 后台保活前台服务开关；默认开启，关闭后 App 退后台会被系统回收、收不到审批通知。 */
    var keepAlive: Boolean
        get() = prefs.getBoolean(KEY_KEEP_ALIVE, true)
        set(value) { prefs.edit().putBoolean(KEY_KEEP_ALIVE, value).apply() }

    override fun load(hostId: String, sessionId: String): String = prefs.getString(draftKey(hostId, sessionId), null).orEmpty()

    override fun save(hostId: String, sessionId: String, text: String) {
        val key = draftKey(hostId, sessionId)
        prefs.edit().putString(key, text).apply()
    }

    override fun clear(hostId: String, sessionId: String) {
        prefs.edit().remove(draftKey(hostId, sessionId)).apply()
    }

    private fun draftKey(hostId: String, sessionId: String): String = "draft:$hostId:$sessionId"

    private fun records(): List<StoredHost> {
        prefs.getString(KEY_HOSTS, null)?.let {
            return wireJson.decodeFromString(ListSerializer(StoredHost.serializer()), it)
        }
        val url = prefs.getString(KEY_URL, null) ?: return emptyList()
        val host = StoredHost(UUID.randomUUID().toString(), "", url)
        // 直接迁移旧密文，沿用原 Keystore 密钥；一次提交后才移除旧字段。
        commit(prefs.edit().putString(KEY_HOSTS, encode(listOf(host))).putString(KEY_ACTIVE, host.id)
            .putString(tokenKey(host.id), prefs.getString(KEY_TOKEN, null)).remove(KEY_URL).remove(KEY_TOKEN))
        return listOf(host)
    }

    private fun encode(hosts: List<StoredHost>): String = wireJson.encodeToString(ListSerializer(StoredHost.serializer()), hosts)

    private fun commit(editor: SharedPreferences.Editor) {
        check(editor.commit()) { "无法保存主机连接，请重试" }
    }

    private fun tokenKey(id: String): String = "token:$id"

    private companion object {
        const val KEY_HOSTS = "hosts"
        const val KEY_ACTIVE = "active-host"
        const val KEY_URL = "url"
        const val KEY_TOKEN = "token"
        const val KEY_KEEP_ALIVE = "keep-alive"
    }
}

internal interface TokenCipher {
    fun encrypt(plain: String): String
    fun decrypt(stored: String): String?
}

private class KeystoreTokenCipher : TokenCipher {
    private fun key(): SecretKey {
        val store = KeyStore.getInstance(PROVIDER).apply { load(null) }
        (store.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, PROVIDER)
        generator.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return generator.generateKey()
    }

    override fun encrypt(plain: String): String {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val data = cipher.doFinal(plain.toByteArray())
        return Base64.encodeToString(cipher.iv, Base64.NO_WRAP) + ":" + Base64.encodeToString(data, Base64.NO_WRAP)
    }

    override fun decrypt(stored: String): String? {
        val parts = stored.split(":", limit = 2)
        if (parts.size != 2) return null
        return runCatching {
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, Base64.decode(parts[0], Base64.NO_WRAP)))
            String(cipher.doFinal(Base64.decode(parts[1], Base64.NO_WRAP)))
        }.getOrNull()
    }

    private companion object {
        const val PROVIDER = "AndroidKeyStore"
        const val ALIAS = "easyagent-host-token"
        const val TRANSFORMATION = "AES/GCM/NoPadding"
    }
}
