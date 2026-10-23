package com.hwj.easyagent.mobile.ui

/**
 * A small Markdown parser for agent replies, pure Kotlin so it can be unit
 * tested on the JVM. It covers what models actually write: headings, lists,
 * quotes, rules, tables, fenced code and inline emphasis, code and links.
 * Anything it does not recognise stays as plain text; it never drops content.
 */
data class Span(
    val text: String,
    val bold: Boolean = false,
    val italic: Boolean = false,
    val code: Boolean = false,
    val strike: Boolean = false,
    val link: String? = null,
)

sealed class MdBlock {
    data class Heading(val level: Int, val spans: List<Span>) : MdBlock()
    data class Paragraph(val spans: List<Span>) : MdBlock()
    data class ListItem(val ordered: Boolean, val marker: String, val depth: Int, val spans: List<Span>) : MdBlock()
    data class Quote(val spans: List<Span>) : MdBlock()
    data class Code(val language: String, val text: String) : MdBlock()
    data class Table(val header: List<List<Span>>, val rows: List<List<List<Span>>>) : MdBlock()
    data object Rule : MdBlock()
}

private val headingRe = Regex("^(#{1,6})\\s+(.*?)\\s*#*\\s*$")
private val ruleRe = Regex("^\\s{0,3}([-*_])(\\s*\\1){2,}\\s*$")
private val listRe = Regex("^(\\s*)([-*+]|\\d{1,9}[.)])\\s+(.*)$")
private val tableSepRe = Regex("^\\s*\\|?\\s*:?-{1,}:?\\s*(\\|\\s*:?-{1,}:?\\s*)*\\|?\\s*$")

fun parseMarkdown(text: String): List<MdBlock> {
    val lines = text.replace("\r\n", "\n").split('\n')
    val blocks = ArrayList<MdBlock>()
    val paragraph = ArrayList<String>()
    fun flush() {
        if (paragraph.isNotEmpty()) {
            blocks += MdBlock.Paragraph(parseInline(paragraph.joinToString("\n")))
            paragraph.clear()
        }
    }
    var i = 0
    while (i < lines.size) {
        val line = lines[i]
        val trimmed = line.trimStart()
        when {
            trimmed.startsWith("```") || trimmed.startsWith("~~~") -> {
                flush()
                val fence = trimmed.take(3)
                val language = trimmed.drop(3).trim().substringBefore(' ')
                val body = ArrayList<String>()
                i++
                // A fence still being streamed has no closing line yet: take the rest.
                while (i < lines.size && !lines[i].trimStart().startsWith(fence)) body += lines[i++]
                blocks += MdBlock.Code(language, body.joinToString("\n"))
            }
            line.isBlank() -> flush()
            headingRe.matches(line) -> {
                flush()
                val m = headingRe.find(line)!!
                blocks += MdBlock.Heading(m.groupValues[1].length, parseInline(m.groupValues[2]))
            }
            ruleRe.matches(line) -> {
                flush()
                blocks += MdBlock.Rule
            }
            trimmed.startsWith(">") -> {
                flush()
                val quote = ArrayList<String>()
                while (i < lines.size && lines[i].trimStart().startsWith(">")) {
                    quote += lines[i].trimStart().removePrefix(">").removePrefix(" ")
                    i++
                }
                blocks += MdBlock.Quote(parseInline(quote.joinToString("\n")))
                continue
            }
            '|' in line && i + 1 < lines.size && tableSepRe.matches(lines[i + 1]) && '|' in lines[i + 1] -> {
                flush()
                val header = cells(line)
                val rows = ArrayList<List<List<Span>>>()
                i += 2
                while (i < lines.size && '|' in lines[i] && lines[i].isNotBlank()) rows += cells(lines[i++]).let { row ->
                    List(header.size) { col -> row.getOrElse(col) { emptyList() } }
                }
                blocks += MdBlock.Table(header, rows)
                continue
            }
            listRe.matches(line) -> {
                flush()
                val m = listRe.find(line)!!
                val marker = m.groupValues[2]
                val ordered = marker[0].isDigit()
                val itemLines = arrayListOf(m.groupValues[3])
                i++
                // Indented lines that are not a new item continue this item.
                while (i < lines.size && lines[i].isNotBlank() && !listRe.matches(lines[i]) && lines[i].startsWith("  ")) {
                    itemLines += lines[i++].trim()
                }
                blocks += MdBlock.ListItem(ordered, if (ordered) marker.dropLast(1) + "." else "•", m.groupValues[1].replace("\t", "  ").length / 2, parseInline(itemLines.joinToString("\n")))
                continue
            }
            else -> paragraph += line
        }
        i++
    }
    flush()
    return blocks
}

private fun cells(line: String): List<List<Span>> {
    var row = line.trim()
    if (row.startsWith("|")) row = row.drop(1)
    if (row.endsWith("|") && !row.endsWith("\\|")) row = row.dropLast(1)
    return row.split(Regex("(?<!\\\\)\\|")).map { parseInline(it.trim().replace("\\|", "|")) }
}

private data class Style(val bold: Boolean = false, val italic: Boolean = false, val strike: Boolean = false, val link: String? = null)

// ASCII URL characters only, so Chinese text right after a link is not swallowed,
// and never ending in sentence punctuation.
private val autoLinkRe = Regex("https?://[A-Za-z0-9\\-._~:/?#@!$&'*+,;=%]*[A-Za-z0-9/\\-_~=#%&+]")

fun parseInline(text: String): List<Span> {
    val out = ArrayList<Span>()
    inline(text, Style(), out)
    // Merge neighbours with the same style so renderers get fewer spans.
    val merged = ArrayList<Span>()
    for (span in out) {
        val last = merged.lastOrNull()
        if (last != null && last.copy(text = "") == span.copy(text = "")) merged[merged.size - 1] = last.copy(text = last.text + span.text)
        else if (span.text.isNotEmpty()) merged += span
    }
    return merged
}

private fun inline(text: String, style: Style, out: MutableList<Span>) {
    val plain = StringBuilder()
    fun emit() {
        if (plain.isEmpty()) return
        val value = plain.toString()
        plain.setLength(0)
        if (style.link != null) {
            out += Span(value, style.bold, style.italic, strike = style.strike, link = style.link)
            return
        }
        var at = 0
        for (m in autoLinkRe.findAll(value)) {
            if (m.range.first > at) out += Span(value.substring(at, m.range.first), style.bold, style.italic, strike = style.strike)
            out += Span(m.value, style.bold, style.italic, strike = style.strike, link = m.value)
            at = m.range.last + 1
        }
        if (at < value.length) out += Span(value.substring(at), style.bold, style.italic, strike = style.strike)
    }
    var i = 0
    while (i < text.length) {
        val c = text[i]
        if (c == '\\' && i + 1 < text.length && text[i + 1] in "\\`*_~[]()#|>-+.!") {
            plain.append(text[i + 1]); i += 2; continue
        }
        if (c == '`') {
            val ticks = text.drop(i).takeWhile { it == '`' }.length
            val end = text.indexOf("`".repeat(ticks), i + ticks)
            if (end > 0) {
                emit()
                out += Span(text.substring(i + ticks, end).trim(), style.bold, style.italic, code = true, strike = style.strike, link = style.link)
                i = end + ticks; continue
            }
        }
        if (c == '[') {
            val close = matching(text, i, '[', ']')
            if (close > 0 && close + 1 < text.length && text[close + 1] == '(') {
                val end = text.indexOf(')', close + 2)
                if (end > 0) {
                    val url = text.substring(close + 2, end).trim().substringBefore(' ')
                    if (url.startsWith("http://") || url.startsWith("https://") || url.startsWith("mailto:")) {
                        emit()
                        inline(text.substring(i + 1, close), style.copy(link = url), out)
                        i = end + 1; continue
                    }
                }
            }
        }
        val pair = when {
            text.startsWith("**", i) || text.startsWith("__", i) -> text.substring(i, i + 2)
            text.startsWith("~~", i) -> "~~"
            c == '*' -> "*"
            c == '_' && (i == 0 || !text[i - 1].isLetterOrDigit()) -> "_"
            else -> null
        }
        if (pair != null) {
            val end = closing(text, i + pair.length, pair)
            if (end > i + pair.length) {
                emit()
                val inner = text.substring(i + pair.length, end)
                val next = when (pair) {
                    "**", "__" -> style.copy(bold = true)
                    "~~" -> style.copy(strike = true)
                    else -> style.copy(italic = true)
                }
                inline(inner, next, out)
                i = end + pair.length; continue
            }
        }
        plain.append(c)
        i++
    }
    emit()
}

/** Index of the delimiter closing [pair] that starts at [from], or -1. */
private fun closing(text: String, from: Int, pair: String): Int {
    if (from >= text.length || text[from].isWhitespace()) return -1
    var i = from
    while (i < text.length) {
        var at = text.indexOf(pair, i)
        if (at < 0) return -1
        // In "***" closing "**", the inner "*" closes first: take the last two.
        if (pair.length == 2) while (text.getOrNull(at + 2) == pair[0]) at++
        // "*" must not take one half of "**".
        val doubled = pair.length == 1 && (text.getOrNull(at + 1) == pair[0] || text.getOrNull(at - 1) == pair[0] && at - 1 >= from)
        val wordUnderscore = pair == "_" && text.getOrNull(at + 1)?.isLetterOrDigit() == true
        if (!text[at - 1].isWhitespace() && !doubled && !wordUnderscore) return at
        i = at + pair.length
    }
    return -1
}

private fun matching(text: String, open: Int, a: Char, b: Char): Int {
    var depth = 0
    for (i in open until text.length) {
        if (text[i] == a) depth++
        if (text[i] == b && --depth == 0) return i
    }
    return -1
}
