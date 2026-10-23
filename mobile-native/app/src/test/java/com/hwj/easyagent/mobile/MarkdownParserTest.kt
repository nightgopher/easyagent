package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.ui.MdBlock
import com.hwj.easyagent.mobile.ui.Span
import com.hwj.easyagent.mobile.ui.parseInline
import com.hwj.easyagent.mobile.ui.parseMarkdown
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class MarkdownParserTest {
    private fun plain(spans: List<Span>) = spans.joinToString("") { it.text }

    @Test
    fun `blocks agents commonly write`() {
        val blocks = parseMarkdown(
            """
            ## 结论
            部署**成功**，共 3 步：

            1. 构建镜像
            2. 推送 `registry`
               继续说明
            - 子项 A
              - 嵌套 B

            > 注意：需要重启

            ---
            ```bash
            go test ./...
            ```
            """.trimIndent(),
        )
        assertEquals(MdBlock.Heading(2, listOf(Span("结论"))), blocks[0])
        assertEquals(listOf(Span("部署"), Span("成功", bold = true), Span("，共 3 步：")), (blocks[1] as MdBlock.Paragraph).spans)
        val first = blocks[2] as MdBlock.ListItem
        assertTrue(first.ordered)
        assertEquals("1.", first.marker)
        val second = blocks[3] as MdBlock.ListItem
        assertEquals(listOf(Span("推送 "), Span("registry", code = true), Span("\n继续说明")), second.spans)
        assertEquals(0, (blocks[4] as MdBlock.ListItem).depth)
        assertEquals(1, (blocks[5] as MdBlock.ListItem).depth)
        assertEquals("注意：需要重启", plain((blocks[6] as MdBlock.Quote).spans))
        assertEquals(MdBlock.Rule, blocks[7])
        assertEquals(MdBlock.Code("bash", "go test ./..."), blocks[8])
        assertEquals(9, blocks.size)
    }

    @Test
    fun `unclosed fence while streaming keeps the rest as code`() {
        val blocks = parseMarkdown("看这里：\n```go\nfunc main() {\n")
        assertEquals(MdBlock.Code("go", "func main() {\n"), blocks.last())
    }

    @Test
    fun `tables keep columns and pad short rows`() {
        val table = parseMarkdown("| 名称 | 状态 |\n|---|:-:|\n| api | **运行** |\n| web |\n").single() as MdBlock.Table
        assertEquals(listOf("名称", "状态"), table.header.map(::plain))
        assertEquals(listOf(Span("运行", bold = true)), table.rows[0][1])
        assertEquals(emptyList<Span>(), table.rows[1][1])
    }

    @Test
    fun `inline emphasis links and escapes`() {
        assertEquals(
            listOf(Span("a "), Span("b", italic = true), Span(" "), Span("c", strike = true), Span(" "), Span("文档", link = "https://x.dev/a"), Span(" *不是斜体*")),
            parseInline("a *b* ~~c~~ [文档](https://x.dev/a) \\*不是斜体\\*"),
        )
        assertEquals(listOf(Span("粗", bold = true), Span("斜", bold = true, italic = true)), parseInline("**粗*斜***"))
    }

    @Test
    fun `snake_case and lone stars stay literal`() {
        assertEquals(listOf(Span("run_test_case and 2 * 3 = 6")), parseInline("run_test_case and 2 * 3 = 6"))
    }

    @Test
    fun `bare urls become links without trailing punctuation`() {
        val spans = parseInline("见 https://github.com/a/b。然后")
        assertEquals(Span("https://github.com/a/b", link = "https://github.com/a/b"), spans[1])
        assertEquals("。然后", spans[2].text)
    }

    @Test
    fun `unsafe link schemes stay text`() {
        assertEquals("[点我](javascript:alert(1))", plain(parseInline("[点我](javascript:alert(1))")))
    }
}
