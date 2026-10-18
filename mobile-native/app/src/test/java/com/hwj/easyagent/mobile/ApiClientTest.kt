package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.ApiClient
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class ApiClientTest {
    @Test
    fun `normalize keeps scheme host port and path`() {
        assertEquals("http://host:8080", ApiClient.normalize(" http://host:8080/ "))
        assertEquals("https://host", ApiClient.normalize("https://host"))
        assertEquals("http://host:8080/api", ApiClient.normalize("http://host:8080/api/"))
    }

    @Test
    fun `normalize rejects credentials query fragment and bare host`() {
        for (raw in listOf("http://user:secret@host", "http://host/?x=1", "http://host/#frag", "host:8080", "ftp://host")) {
            assertThrows(raw, IllegalArgumentException::class.java) { ApiClient.normalize(raw) }
        }
    }

    @Test
    fun `session path escapes id`() {
        assertEquals("/sessions/a%20b%2Fc", ApiClient.sessionPath("a b/c"))
    }
}
