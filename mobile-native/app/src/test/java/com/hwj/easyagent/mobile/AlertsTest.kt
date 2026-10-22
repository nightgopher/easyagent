package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.Alert
import com.hwj.easyagent.mobile.backend.ChatSession
import com.hwj.easyagent.mobile.backend.Envelope
import com.hwj.easyagent.mobile.backend.alertState
import com.hwj.easyagent.mobile.backend.alertsFor
import com.hwj.easyagent.mobile.backend.wireJson
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class AlertsTest {
    private fun ChatSession.apply(json: String): List<Alert> {
        val before = alertState()
        reduce(wireJson.decodeFromString(Envelope.serializer(), json))
        return alertsFor(before, this)
    }

    private fun running(): ChatSession = ChatSession("s1").also {
        it.apply("""{"type":"accepted","session_id":"s1","run_id":"r1","request_id":"q1","state":"running","prompt":"部署","seq":1}""")
        assertTrue(it.running)
    }

    @Test
    fun `new approval alerts once`() {
        val s = running()
        val confirmation = """{"type":"confirmation","session_id":"s1","run_id":"r1","seq":2,"confirmation":{"confirmation_id":"c1","tool_name":"bash","description":"即将执行 shell 命令（递归删除）"}}"""
        val alerts = s.apply(confirmation)
        assertEquals(listOf(Alert.Approval("s1", "c1", "bash", "即将执行 shell 命令（递归删除）")), alerts)
        // The same approval re-sent after reconnecting does not alert again.
        val again = s.apply(confirmation.replace("\"seq\":2", "\"seq\":3"))
        assertTrue(again.isEmpty())
    }

    @Test
    fun `finished run alerts with its state`() {
        for (state in listOf("completed", "failed", "interrupted")) {
            val s = running()
            val alerts = s.apply("""{"type":"status","session_id":"s1","run_id":"r1","seq":5,"state":"$state"}""")
            assertEquals(listOf(Alert.RunEnded("s1", state, "")), alerts)
        }
    }

    @Test
    fun `idle sessions and progress do not alert`() {
        val idle = ChatSession("s1")
        assertTrue(idle.apply("""{"type":"status","session_id":"s1","run_id":"r1","seq":1,"state":"completed"}""").isEmpty())
        val s = running()
        assertTrue(s.apply("""{"type":"status","session_id":"s1","run_id":"r1","seq":2,"state":"running","streaming":true}""").isEmpty())
    }
}
