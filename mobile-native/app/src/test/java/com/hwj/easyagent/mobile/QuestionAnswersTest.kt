package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.UserOption
import com.hwj.easyagent.mobile.backend.UserQuestion
import com.hwj.easyagent.mobile.backend.buildAnswers
import com.hwj.easyagent.mobile.backend.questionAnswered
import com.hwj.easyagent.mobile.backend.questionsFromArgs
import com.hwj.easyagent.mobile.backend.wireJson
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** ask_user_question 参数解析与 answers 组装的协议行为，与服务端 ValidateAskUserAnswers 对齐。 */
class QuestionAnswersTest {
    private val single = UserQuestion(
        question = "用哪种部署方式？",
        header = "部署",
        options = listOf(UserOption("蓝绿", "零停机"), UserOption("滚动")),
    )
    private val multi = UserQuestion(
        question = "要跑哪些检查？",
        options = listOf(UserOption("lint"), UserOption("test")),
        multiSelect = true,
    )

    @Test
    fun `parses questions from ask_user args`() {
        val args = wireJson.parseToJsonElement(
            """{"questions":[{"question":"用哪种部署方式？","header":"部署","options":[{"label":"蓝绿","description":"零停机"},{"label":"滚动"}]}]}""",
        )
        val parsed = questionsFromArgs(args)!!
        assertEquals(1, parsed.size)
        assertEquals("部署", parsed[0].header)
        assertEquals("用哪种部署方式？", parsed[0].question)
        assertEquals("蓝绿", parsed[0].options[0].label)
        assertEquals("零停机", parsed[0].options[0].description)
        assertFalse(parsed[0].multiSelect)
    }

    @Test
    fun `non question args or empty lists return null and fall back to plain approval`() {
        assertNull(questionsFromArgs(null))
        assertNull(questionsFromArgs(wireJson.parseToJsonElement("""{"command":"ls"}""")))
        assertNull(questionsFromArgs(wireJson.parseToJsonElement("""{"questions":[]}""")))
        assertNull(questionsFromArgs(wireJson.parseToJsonElement("""{"questions":"bad"}""")))
        // 选项缺失等非法参数由服务端校验拦截；客户端解析宽容，表单仍可经「其他」作答或跳过。
        assertEquals(1, questionsFromArgs(wireJson.parseToJsonElement("""{"questions":[{"question":"Q?"}]}"""))!!.size)
    }

    @Test
    fun `single select requires exactly one of option or custom text`() {
        assertFalse(questionAnswered(single, emptySet(), ""))
        assertTrue(questionAnswered(single, setOf("蓝绿"), ""))
        assertTrue(questionAnswered(single, emptySet(), "容器里手动跑"))
        assertFalse(questionAnswered(single, setOf("蓝绿", "滚动"), ""))
        assertFalse(questionAnswered(single, setOf("蓝绿"), "容器里手动跑"))
    }

    @Test
    fun `multi select needs at least one item counting custom text`() {
        assertTrue(questionAnswered(multi, setOf("lint"), ""))
        assertTrue(questionAnswered(multi, setOf("lint", "test"), ""))
        assertTrue(questionAnswered(multi, emptySet(), "加一个冒烟"))
        assertFalse(questionAnswered(multi, emptySet(), ""))
    }

    @Test
    fun `buildAnswers rejects incomplete answers and assembles in question order`() {
        assertNull(buildAnswers(listOf(single, multi), listOf(setOf("蓝绿")), listOf("", "")))
        assertNull(buildAnswers(listOf(single), listOf(emptySet()), listOf("")))
        val answers = buildAnswers(listOf(single, multi), listOf(setOf("蓝绿"), emptySet()), listOf("", "加一个冒烟"))!!
        assertEquals("""[{"selected":["蓝绿"]},{"selected":[],"text":"加一个冒烟"}]""", answers.toString())
    }
}
