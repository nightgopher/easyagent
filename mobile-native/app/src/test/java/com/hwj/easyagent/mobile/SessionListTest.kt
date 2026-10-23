package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.SessionSummary
import com.hwj.easyagent.mobile.backend.filterSessions
import com.hwj.easyagent.mobile.backend.groupSessions
import com.hwj.easyagent.mobile.backend.relativeTime
import com.hwj.easyagent.mobile.backend.wireJson
import kotlinx.serialization.builtins.ListSerializer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant
import java.time.ZoneId

class SessionListTest {
    private val zone = ZoneId.of("Asia/Shanghai")
    private val now = Instant.parse("2026-10-23T06:00:00Z") // 14:00 local

    @Test
    fun `relative times`() {
        val t = now.epochSecond
        assertEquals("刚刚", relativeTime(t - 20, now, zone))
        assertEquals("5 分钟前", relativeTime(t - 300, now, zone))
        assertEquals("3 小时前", relativeTime(t - 3 * 3600, now, zone))
        assertEquals("昨天", relativeTime(t - 20 * 3600, now, zone))
        assertEquals("10月20日", relativeTime(t - 3 * 86400, now, zone))
        assertEquals("", relativeTime(0, now, zone))
    }

    @Test
    fun `search matches every word in title or project`() {
        val list = listOf(
            SessionSummary(id = "1", title = "部署 api", workspace = "/srv/shop"),
            SessionSummary(id = "2", title = "修复登录", workspace = "/srv/auth"),
        )
        assertEquals(listOf("1"), filterSessions(list, "SHOP 部署").map { it.id })
        assertEquals(listOf("2"), filterSessions(list, "auth").map { it.id })
        assertEquals(2, filterSessions(list, "  ").size)
    }

    @Test
    fun `groups pinned first then projects by recent activity`() {
        val list = listOf(
            SessionSummary(id = "a", workspace = "/w/old", lastActive = 10),
            SessionSummary(id = "b", workspace = "/w/new", lastActive = 30),
            SessionSummary(id = "c", workspace = "/w/new/", lastActive = 20),
            SessionSummary(id = "p", workspace = "/w/old", lastActive = 5, pinned = true),
        )
        val groups = groupSessions(list)
        assertEquals(listOf("置顶", "new", "old"), groups.map { it.title })
        assertEquals(listOf("b", "c"), groups[1].sessions.map { it.id })
    }

    @Test
    fun `a single project needs no headings`() {
        val groups = groupSessions(listOf(SessionSummary(id = "a", workspace = "/w/x"), SessionSummary(id = "b", workspace = "/w/x")))
        assertEquals(1, groups.size)
        assertEquals("", groups[0].title)
    }

    @Test
    fun `run state fields decode and older hosts default to idle`() {
        val list = wireJson.decodeFromString(
            ListSerializer(SessionSummary.serializer()),
            """[{"id":"a","run_state":"waiting_confirmation","pending_confirmations":2},{"id":"b"}]""",
        )
        assertTrue(list[0].running)
        assertEquals(2, list[0].pendingConfirmations)
        assertFalse(list[1].running)
    }
}
