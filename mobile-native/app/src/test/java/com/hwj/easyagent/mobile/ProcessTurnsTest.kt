package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.*
import org.junit.Assert.*
import org.junit.Test

class ProcessTurnsTest {
    private fun message(id: Long, kind: String = "", text: String = "body") = ChatMessage(id).also { it.kind = kind; it.text = text }
    @Test fun `completed turn promotes only final answer and folds intermediate work`() {
        val user = message(1).also { it.user = true }
        val thought = message(2, "thought")
        val progress = message(3)
        val tool = message(4, "tool").also { it.status = "完成" }
        val final = message(5)
        val turn = processTurns(listOf(user, thought, progress, tool, final), false).single()
        assertSame(final, turn.answer)
        assertEquals(listOf(thought, progress, tool), turn.process)
        assertFalse(turn.open)
    }
    @Test fun `active failed and unfinished turns never hide the work`() {
        val thought = message(1, "thought")
        val final = message(2)
        assertTrue(processTurns(listOf(thought, final), true).single().open)
        assertNull(processTurns(listOf(thought, final), true).single().answer)
        final.status = "未完成"
        assertTrue(processTurns(listOf(thought, final), false).single().open)
        assertNull(processTurns(listOf(thought, final), false).single().answer)
        assertTrue(processTurns(listOf(thought), false).single().open)
    }
    @Test fun `earlier completed turns remain folded during a later run`() {
        val a = message(1).also { it.user = true }
        val b = message(4).also { it.user = true }
        val turns = processTurns(listOf(a, message(2, "thought"), message(3), b, message(5, "thought")), true)
        assertFalse(turns[0].open)
        assertTrue(turns[1].open)
    }
}
