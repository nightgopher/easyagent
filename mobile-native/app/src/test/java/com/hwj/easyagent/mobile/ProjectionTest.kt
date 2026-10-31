package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.ChatSession
import com.hwj.easyagent.mobile.backend.Envelope
import com.hwj.easyagent.mobile.backend.obj
import com.hwj.easyagent.mobile.backend.str
import com.hwj.easyagent.mobile.backend.wireJson
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ProjectionTest {
    private fun envelope(json: String) = wireJson.decodeFromString(Envelope.serializer(), json)

    @Test
    fun `tool discovery uses existing events and ignores additive context fields`() {
        val usage = wireJson.decodeFromString(com.hwj.easyagent.mobile.backend.ContextUsage.serializer(),
            """{"estimated_tokens":800,"context_window":128000,"window_known":true,"tool_discovery":{"mode":"local","resident":12,"loaded":2,"searchable":498}}""")
        assertEquals(800L, usage.estimatedTokens)
        val s = ChatSession("s1")
        s.reduce(envelope("""{"type":"event","session_id":"s1","seq":1,"event":{"type":"tool_start","tool_call_id":"search-1","tool_name":"search_tools","tool_args":{"query":"审批"}}}"""))
        s.reduce(envelope("""{"type":"event","session_id":"s1","seq":2,"event":{"type":"tool_end","tool_call_id":"search-1","tool_name":"search_tools","tool_result":"工具可用了","tool_details":{"kind":"tool_discovery","version":1,"selected":[{"name":"mcp__oa__pending"}]}}}"""))
        val tool = s.messages.last { it.tool == "search_tools" }
        assertEquals("完成", tool.status)
        assertEquals("工具可用了", tool.result)
    }

    @Test
    fun `snapshot history builds user assistant tool`() {
        val s = ChatSession("s1")
        s.reduce(
            envelope(
                """
                {"type":"snapshot","session_id":"s1","seq":5,"messages":[
                  {"role":"user","content":"帮我看看","entry_id":"e1"},
                  {"role":"assistant","thinking":"想一想","content":[{"type":"text","text":"好的"}],
                   "tool_calls":[{"id":"t1","name":"bash","args":"{\"command\":\"ls\"}"}],"entry_id":"e2"},
                  {"role":"tool","tool_call_id":"t1","content":"file1\nfile2","duration_ms":120.5}
                ]}
                """.trimIndent(),
            ),
        )
        assertEquals(4, s.messages.size)
        val user = s.messages[0]
        assertTrue(user.user)
        assertEquals("帮我看看", user.text)
        val thought = s.messages[1]
        assertEquals("thought", thought.kind)
        assertEquals("想一想", thought.text)
        val assistant = s.messages[2]
        assertEquals("好的", assistant.text)
        val tool = s.messages[3]
        assertEquals("tool", tool.kind)
        assertEquals("bash", tool.tool)
        assertEquals("完成", tool.status)
        assertEquals("file1\nfile2", tool.result)
        assertEquals(120.5, tool.durationMs, 0.0)
        assertEquals("ls", tool.args.obj()["command"].str())
        assertFalse(s.running)
    }

    @Test
    fun `streaming events accumulate into a single reply`() {
        val s = ChatSession("s1")
        fun feed(json: String) = s.reduce(envelope(json))
        feed("""{"type":"accepted","session_id":"s1","seq":6,"request_id":"r1","run_id":"run1","state":"running","prompt":"你好"}""")
        assertEquals(2, s.messages.size) // 用户消息 + 等待占位
        assertTrue(s.running)
        assertTrue(s.messages[1].isWaiting)
        feed("""{"type":"event","session_id":"s1","seq":7,"run_id":"run1","event":{"type":"text_delta","text_delta":"你"}}""")
        feed("""{"type":"event","session_id":"s1","seq":8,"run_id":"run1","event":{"type":"text_delta","text_delta":"好"}}""")
        assertEquals("你好", s.messages[1].text)
        feed("""{"type":"accepted","session_id":"s1","seq":6,"request_id":"r1","run_id":"run1","state":"running","prompt":"你好"}""")
        assertEquals(2, s.messages.size) // 重复 seq 被忽略
        feed("""{"type":"event","session_id":"s1","seq":9,"run_id":"run1","event":{"type":"tool_start","tool_call_id":"t9","tool_name":"read","tool_args":{"path":"/tmp/x"}}}""")
        val tool = s.messages[2]
        assertEquals("read", tool.tool)
        assertEquals("执行中", tool.status)
        feed("""{"type":"event","session_id":"s1","seq":10,"run_id":"run1","event":{"type":"tool_end","tool_call_id":"t9","tool_result":"内容","duration_ms":50.0}}""")
        assertEquals("完成", tool.status)
        assertEquals("内容", tool.result)
        feed("""{"type":"status","session_id":"s1","seq":11,"run_id":"run1","state":"completed","streaming":false}""")
        assertFalse(s.running)
        assertFalse(s.messages.any { it.isWaiting })
    }

    @Test
    fun `thinking deltas become thought message`() {
        val s = ChatSession("s1")
        s.reduce(envelope("""{"type":"accepted","session_id":"s1","seq":1,"run_id":"run1","state":"running","prompt":"hi"}"""))
        assertTrue(s.messages[1].isWaiting) // 占位会被首个 thinking_delta 转成思考消息
        s.reduce(envelope("""{"type":"event","session_id":"s1","seq":2,"run_id":"run1","event":{"type":"thinking_delta","text_delta":"思考"}}"""))
        s.reduce(envelope("""{"type":"event","session_id":"s1","seq":3,"run_id":"run1","event":{"type":"thinking_delta","text_delta":"内容"}}"""))
        s.reduce(envelope("""{"type":"event","session_id":"s1","seq":4,"run_id":"run1","event":{"type":"text_delta","text_delta":"回答"}}"""))
        assertEquals("思考内容", s.messages[1].text)
        assertEquals("thought", s.messages[1].kind)
        assertEquals("回答", s.messages[2].text)
        assertEquals("", s.messages[2].kind)
    }

    @Test
    fun `confirmations appear and clear`() {
        val s = ChatSession("s1")
        s.reduce(envelope("""{"type":"confirmation","session_id":"s1","seq":1,"confirmation":{"confirmation_id":"c1","tool_name":"write","description":"写文件"}}"""))
        assertTrue(s.pending)
        assertEquals(1, s.confirmations.size)
        assertEquals("write", s.confirmations[0].toolName)
        s.reduce(envelope("""{"type":"confirmation","session_id":"s1","seq":1,"confirmation":{"confirmation_id":"c1","tool_name":"write"}}"""))
        assertEquals(1, s.confirmations.size) // 不重复
        s.reduce(envelope("""{"type":"confirmed","session_id":"s1","seq":2,"confirmation_id":"c1"}"""))
        assertFalse(s.pending)
    }

    @Test
    fun `queue envelope replaces newer revisions only`() {
        val s = ChatSession("s1")
        s.reduce(envelope("""{"type":"snapshot","session_id":"s1","seq":1,"queue":{"items":[{"id":"q1","prompt":"等等"}],"paused":false,"revision":3}}"""))
        assertEquals(1, s.queue.items.size)
        assertEquals(3, s.queue.revision)
        s.reduce(envelope("""{"type":"snapshot","session_id":"s1","seq":2,"queue":{"items":[{"id":"q1","prompt":"等等"}],"paused":false,"revision":2}}"""))
        assertEquals(3, s.queue.revision) // 旧版本不回退
    }

    @Test
    fun `snapshot events are applied on top of history`() {
        val s = ChatSession("s1")
        s.reduce(
            envelope(
                """
                {"type":"snapshot","session_id":"s1","seq":9,"reset":true,"run":{"run_id":"run2","state":"running","request_id":"r2"},"messages":[
                  {"role":"user","content":"继续"}
                ],"events":[
                  {"type":"text_delta","text_delta":"部分"}
                ]}
                """.trimIndent(),
            ),
        )
        assertTrue(s.running)
        assertEquals("继续", s.messages[0].text)
        assertEquals("部分", s.messages[1].text)
        assertTrue(s.messages[1].isWaiting || s.messages[1].text.isNotEmpty())
    }
}
