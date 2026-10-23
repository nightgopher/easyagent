package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.withLink
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.delay

/**
 * Renders an agent reply as Markdown (see [parseMarkdown]): headings, lists,
 * quotes, tables, rules, code blocks with a copy button, and inline emphasis,
 * code and links. Text stays selectable; links open in the browser.
 */
@Composable
fun MarkdownView(text: String, modifier: Modifier = Modifier) {
    val blocks = remember(text) { parseMarkdown(text) }
    SelectionContainer(modifier) {
        Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
            for (block in blocks) MarkdownBlock(block)
        }
    }
}

@Composable
private fun MarkdownBlock(block: MdBlock) {
    val type = MaterialTheme.typography
    when (block) {
        is MdBlock.Heading -> Text(
            annotated(block.spans),
            style = when (block.level) {
                1 -> type.titleLarge
                2 -> type.titleMedium
                else -> type.titleSmall
            },
            modifier = Modifier.padding(top = 4.dp),
        )
        is MdBlock.Paragraph -> Text(annotated(block.spans), style = type.bodyLarge)
        is MdBlock.ListItem -> Row(modifier = Modifier.padding(start = (block.depth * 16).dp)) {
            Text(
                block.marker,
                style = type.bodyLarge,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.widthIn(min = if (block.ordered) 24.dp else 16.dp),
            )
            Text(annotated(block.spans), style = type.bodyLarge)
        }
        is MdBlock.Quote -> Row(modifier = Modifier.height(IntrinsicSize.Min)) {
            Box(Modifier.width(3.dp).fillMaxHeight().background(MaterialTheme.colorScheme.outlineVariant))
            Text(
                annotated(block.spans),
                style = type.bodyLarge,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(start = 10.dp),
            )
        }
        is MdBlock.Code -> CodeBlock(block.text, modifier = Modifier.fillMaxWidth(), language = block.language)
        is MdBlock.Table -> MarkdownTable(block)
        MdBlock.Rule -> HorizontalDivider(Modifier.padding(vertical = 4.dp))
    }
}

/** Laid out column by column so cells line up; wide tables scroll sideways. */
@Composable
private fun MarkdownTable(table: MdBlock.Table) {
    val border = MaterialTheme.colorScheme.outlineVariant
    Surface(shape = RoundedCornerShape(8.dp), color = Color.Transparent, border = BorderStroke(1.dp, border)) {
        Row(Modifier.horizontalScroll(rememberScrollState())) {
            table.header.indices.forEach { col ->
                Column(Modifier.width(IntrinsicSize.Max)) {
                    Text(
                        annotated(table.header[col]),
                        style = MaterialTheme.typography.labelLarge,
                        softWrap = false,
                        modifier = Modifier.fillMaxWidth().background(MaterialTheme.colorScheme.surfaceVariant).padding(horizontal = 10.dp, vertical = 6.dp),
                    )
                    table.rows.forEach { row ->
                        HorizontalDivider(color = border)
                        Text(
                            annotated(row[col]),
                            style = MaterialTheme.typography.bodyMedium,
                            softWrap = false,
                            modifier = Modifier.padding(horizontal = 10.dp, vertical = 6.dp),
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun annotated(spans: List<Span>): AnnotatedString {
    val codeBackground = MaterialTheme.colorScheme.surfaceVariant
    val linkStyles = TextLinkStyles(SpanStyle(color = MaterialTheme.colorScheme.primary, textDecoration = TextDecoration.Underline))
    return buildAnnotatedString {
        for (span in spans) {
            val style = SpanStyle(
                fontWeight = if (span.bold) FontWeight.SemiBold else null,
                fontStyle = if (span.italic) FontStyle.Italic else null,
                textDecoration = if (span.strike) TextDecoration.LineThrough else null,
                fontFamily = if (span.code) FontFamily.Monospace else null,
                background = if (span.code) codeBackground else Color.Unspecified,
                fontSize = if (span.code) 0.9.em else TextUnit.Unspecified,
            )
            val link = span.link
            if (link != null) {
                withLink(LinkAnnotation.Url(link, linkStyles)) { withStyle(style) { append(span.text) } }
            } else {
                withStyle(style) { append(span.text) }
            }
        }
    }
}

/** Monospace text that scrolls sideways; with a [language] it gets a header and a copy button. */
@Composable
fun CodeBlock(code: String, modifier: Modifier = Modifier, language: String? = null) {
    Surface(
        modifier = modifier,
        shape = RoundedCornerShape(8.dp),
        color = MaterialTheme.colorScheme.surfaceVariant,
    ) {
        Column {
            if (language != null) {
                val clipboard = LocalClipboardManager.current
                var copied by remember { mutableStateOf(false) }
                LaunchedEffect(copied) {
                    if (copied) {
                        delay(2_000)
                        copied = false
                    }
                }
                Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(start = 10.dp)) {
                    Text(language, style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
                    IconButton(onClick = { clipboard.setText(AnnotatedString(code)); copied = true }, modifier = Modifier.size(36.dp)) {
                        Icon(
                            if (copied) Icons.Filled.Check else Icons.Filled.ContentCopy,
                            contentDescription = if (copied) "已复制" else "复制代码",
                            modifier = Modifier.size(16.dp),
                            tint = if (copied) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                }
            }
            Text(
                code,
                fontFamily = FontFamily.Monospace,
                fontSize = 12.sp,
                lineHeight = 17.sp,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                softWrap = false,
                modifier = Modifier
                    .horizontalScroll(rememberScrollState())
                    .padding(start = 10.dp, end = 10.dp, bottom = 10.dp, top = if (language != null) 0.dp else 10.dp),
            )
        }
    }
}
