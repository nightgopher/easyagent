package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/** 轻量 Markdown：只把 ``` 围栏代码块单独渲染为等宽区域，其余按原文显示。 */
@Composable
fun MarkdownLite(text: String, modifier: Modifier = Modifier) {
    SelectionContainer(modifier) {
        Column {
            for (segment in splitFences(text)) {
                if (segment.code) {
                    CodeBlock(segment.text, modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp))
                } else {
                    Text(segment.text, style = MaterialTheme.typography.bodyLarge)
                }
            }
        }
    }
}

@Composable
fun CodeBlock(code: String, modifier: Modifier = Modifier) {
    Surface(
        modifier = modifier,
        shape = RoundedCornerShape(8.dp),
        color = MaterialTheme.colorScheme.surfaceVariant,
    ) {
        Text(
            code,
            fontFamily = FontFamily.Monospace,
            fontSize = 12.sp,
            lineHeight = 17.sp,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.horizontalScroll(rememberScrollState()).padding(10.dp),
        )
    }
}

class TextSegment(val text: String, val code: Boolean)

fun splitFences(text: String): List<TextSegment> {
    val segments = ArrayList<TextSegment>()
    val buffer = StringBuilder()
    var inCode = false
    for (line in text.lineSequence()) {
        if (line.trimStart().startsWith("```")) {
            if (buffer.isNotEmpty() || inCode) {
                segments.add(TextSegment(buffer.toString().trimEnd('\n'), inCode))
                buffer.setLength(0)
            }
            inCode = !inCode
            continue
        }
        buffer.append(line).append('\n')
    }
    if (buffer.isNotEmpty()) segments.add(TextSegment(buffer.toString().trimEnd('\n'), inCode))
    return segments.filter { it.text.isNotEmpty() }
}
