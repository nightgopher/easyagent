package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.ChatMessage
import com.hwj.easyagent.mobile.ui.remaining
import com.hwj.easyagent.mobile.ui.toolNames
import com.hwj.easyagent.mobile.ui.toolRuns
import org.junit.Assert.assertEquals
import org.junit.Test
import java.time.OffsetDateTime

class ChatEntriesTest {
    private var uid = 0L
    private fun message(kind: String = "", tool: String = "") = ChatMessage(uid++).also { it.kind = kind; it.tool = tool }

    @Test fun consecutiveToolCallsFormOneRun() {
        val list = listOf(message(), message("tool", "bash"), message("tool", "read"), message("tool", "bash"), message(), message("tool", "grep"))
        assertEquals(listOf(1, 3, 1, 1), toolRuns(list).map { it.size })
        assertEquals("bash ×2 · read", toolNames(toolRuns(list)[1]))
    }

    @Test fun remainingShowsDaysPastTwoDays() {
        val now = OffsetDateTime.parse("2026-10-23T10:00:00+08:00")
        assertEquals("还剩 30 分钟", remaining("2026-10-23T10:30:00+08:00", now))
        assertEquals("还剩 47 小时", remaining("2026-10-25T09:00:00+08:00", now))
        assertEquals("还剩 30 天", remaining("2026-11-22T10:00:00+08:00", now))
        assertEquals("已到期", remaining("2026-10-23T09:00:00+08:00", now))
    }
}
