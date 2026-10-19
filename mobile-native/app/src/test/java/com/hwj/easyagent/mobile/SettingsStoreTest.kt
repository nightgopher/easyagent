package com.hwj.easyagent.mobile

import android.content.SharedPreferences
import com.hwj.easyagent.mobile.backend.HostConfig
import com.hwj.easyagent.mobile.backend.SettingsStore
import com.hwj.easyagent.mobile.backend.TokenCipher
import org.junit.Assert.*
import org.junit.Test
import java.util.Base64

class SettingsStoreTest {
    @Test
    fun `legacy single host migrates once without replacing its encrypted token`() {
        val prefs = MemoryPreferences()
        val encrypted = TestTokenCipher.encrypt("legacy-secret")
        prefs.edit().putString("url", "http://mini:8080").putString("token", encrypted).commit()
        val store = SettingsStore(prefs, TestTokenCipher)
        val first = store.load()
        val host = first.hosts.single()
        assertEquals("http://mini:8080", host.url)
        assertEquals("legacy-secret", host.token)
        assertEquals(host.id, first.activeId)
        assertEquals(encrypted, prefs.getString("token:${host.id}", null))
        assertFalse(prefs.contains("url"))
        assertFalse(prefs.contains("token"))
        assertEquals(first, SettingsStore(prefs, TestTokenCipher).load())
    }

    @Test
    fun `multiple hosts retain separate ciphertext selection and edits across restart`() {
        val prefs = MemoryPreferences()
        val store = SettingsStore(prefs, TestTokenCipher)
        val a = HostConfig("http://mini:8080", "secret-a", "a", "Mini")
        val b = HostConfig("http://mac:8080", "secret-b", "b", "Mac")
        store.save(a)
        store.save(b)
        store.select(a.id)
        val restarted = SettingsStore(prefs, TestTokenCipher)
        assertEquals(listOf(a, b), restarted.load().hosts)
        assertEquals(a.id, restarted.load().activeId)
        assertFalse(prefs.getString("hosts", "")!!.contains("secret"))
        assertNotEquals("secret-a", prefs.getString("token:a", null))
        val aCiphertext = prefs.getString("token:a", null)
        val updated = b.copy(name = "Mac Studio", token = "new-secret-b")
        restarted.save(updated)
        assertEquals(listOf(a, updated), restarted.load().hosts)
        assertEquals(aCiphertext, prefs.getString("token:a", null))
        assertEquals(b.id, restarted.load().activeId)
    }

    @Test
    fun `deleting a host clears only its token and selects a surviving host`() {
        val prefs = MemoryPreferences()
        val store = SettingsStore(prefs, TestTokenCipher)
        store.save(HostConfig("http://a", "a-secret", "a"))
        store.save(HostConfig("http://b", "b-secret", "b"))
        store.remove("b")
        assertEquals("a", store.load().activeId)
        assertEquals("a-secret", store.load().hosts.single().token)
        assertFalse(prefs.contains("token:b"))
        store.remove("a")
        assertTrue(store.load().hosts.isEmpty())
        assertNull(store.load().activeId)
        assertFalse(prefs.contains("token:a"))
    }

    @Test
    fun `unreadable token is distinct from intentional anonymous connection and does not damage other hosts`() {
        val prefs = MemoryPreferences()
        val store = SettingsStore(prefs, TestTokenCipher)
        store.save(HostConfig("http://a", "a-secret", "a"))
        store.save(HostConfig("http://b", "", "b"))
        prefs.edit().putString("token:a", "invalid-ciphertext").commit()
        assertNull(store.load().hosts.first().token)
        assertEquals("", store.load().hosts.last().token)
        store.save(HostConfig("http://b", "", "b", "Renamed"))
        assertEquals("invalid-ciphertext", prefs.getString("token:a", null))
        assertNull(store.load().hosts.first().token)
    }

    @Test
    fun `failed encryption and duplicate URL leave saved hosts unchanged`() {
        val prefs = MemoryPreferences()
        val store = SettingsStore(prefs, TestTokenCipher)
        store.save(HostConfig("http://a", "original-secret", "a"))
        val before = prefs.all
        assertThrows(IllegalArgumentException::class.java) { store.save(HostConfig("http://a/", "other", "b")) }
        val broken = SettingsStore(prefs, object : TokenCipher {
            override fun encrypt(plain: String): String = "unreadable"
            override fun decrypt(stored: String): String? = null
        })
        assertThrows(IllegalStateException::class.java) { broken.save(HostConfig("http://b", "other", "b")) }
        assertEquals(before, prefs.all)
    }

    @Test
    fun `failed migration keeps legacy connection recoverable`() {
        val prefs = MemoryPreferences()
        prefs.edit().putString("url", "http://mini").putString("token", TestTokenCipher.encrypt("secret")).commit()
        prefs.failCommit = true
        assertThrows(IllegalStateException::class.java) { SettingsStore(prefs, TestTokenCipher).load() }
        assertTrue(prefs.contains("url"))
        assertTrue(prefs.contains("token"))
        prefs.failCommit = false
        assertEquals("secret", SettingsStore(prefs, TestTokenCipher).load().hosts.single().token)
    }
}

internal object TestTokenCipher : TokenCipher {
    override fun encrypt(plain: String): String = "encrypted:" + Base64.getEncoder().encodeToString(plain.toByteArray())
    override fun decrypt(stored: String): String? = if (stored.startsWith("encrypted:")) {
        runCatching { String(Base64.getDecoder().decode(stored.removePrefix("encrypted:"))) }.getOrNull()
    } else null
}

internal class MemoryPreferences : SharedPreferences {
    private val values = LinkedHashMap<String, Any?>()
    var failCommit = false
    override fun getAll(): MutableMap<String, *> = values.toMutableMap()
    override fun getString(key: String?, defValue: String?): String? = values[key] as? String ?: defValue
    override fun getStringSet(key: String?, defValues: MutableSet<String>?): MutableSet<String>? = defValues
    override fun getInt(key: String?, defValue: Int): Int = values[key] as? Int ?: defValue
    override fun getLong(key: String?, defValue: Long): Long = values[key] as? Long ?: defValue
    override fun getFloat(key: String?, defValue: Float): Float = values[key] as? Float ?: defValue
    override fun getBoolean(key: String?, defValue: Boolean): Boolean = values[key] as? Boolean ?: defValue
    override fun contains(key: String?): Boolean = values.containsKey(key)
    override fun registerOnSharedPreferenceChangeListener(listener: SharedPreferences.OnSharedPreferenceChangeListener?) = Unit
    override fun unregisterOnSharedPreferenceChangeListener(listener: SharedPreferences.OnSharedPreferenceChangeListener?) = Unit
    override fun edit(): SharedPreferences.Editor = object : SharedPreferences.Editor {
        private val changes = LinkedHashMap<String, Any?>()
        private var clearing = false
        override fun putString(key: String, value: String?): SharedPreferences.Editor = apply { changes[key] = value }
        override fun putStringSet(key: String, values: MutableSet<String>?): SharedPreferences.Editor = apply { changes[key] = values }
        override fun putInt(key: String, value: Int): SharedPreferences.Editor = apply { changes[key] = value }
        override fun putLong(key: String, value: Long): SharedPreferences.Editor = apply { changes[key] = value }
        override fun putFloat(key: String, value: Float): SharedPreferences.Editor = apply { changes[key] = value }
        override fun putBoolean(key: String, value: Boolean): SharedPreferences.Editor = apply { changes[key] = value }
        override fun remove(key: String): SharedPreferences.Editor = apply { changes[key] = null }
        override fun clear(): SharedPreferences.Editor = apply { clearing = true }
        override fun apply() { commit() }
        override fun commit(): Boolean {
            if (failCommit) return false
            if (clearing) values.clear()
            for ((key, value) in changes) {
                if (value == null) values.remove(key) else values[key] = value
            }
            return true
        }
    }
}
