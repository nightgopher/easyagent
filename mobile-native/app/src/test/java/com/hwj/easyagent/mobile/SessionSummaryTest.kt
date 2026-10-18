package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.SessionSummary
import com.hwj.easyagent.mobile.backend.wireJson
import org.junit.Assert.assertEquals
import org.junit.Test

class SessionSummaryTest {
    @Test
    fun `session time uses the cores last active unix seconds`() {
        val session = wireJson.decodeFromString(SessionSummary.serializer(), """{"id":"s1","created_at":1,"last_active":86400}""")
        assertEquals("1970-01-02T00:00:00Z", session.updatedAt)
    }

    @Test
    fun `creation time is used when activity time is unavailable`() {
        val session = wireJson.decodeFromString(SessionSummary.serializer(), """{"id":"s1","created_at":1}""")
        assertEquals("1970-01-01T00:00:01Z", session.updatedAt)
        assertEquals("", SessionSummary(id = "empty").updatedAt)
    }
}
