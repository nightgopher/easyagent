package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.hwj.easyagent.mobile.backend.UserQuestion
import com.hwj.easyagent.mobile.backend.buildAnswers
import kotlinx.serialization.json.JsonElement

/**
 * ask_user_question 的回答表单：每题一组选项（单选圆点 / 多选勾选框），
 * 末尾固定一个「其他」自填项；单选时选选项与填内容互斥，与服务端校验一致。
 */
@Composable
fun QuestionForm(questions: List<UserQuestion>, onConfirm: (JsonElement) -> Unit, onSkip: () -> Unit) {
    val selections = remember(questions) { List(questions.size) { mutableStateOf(emptySet<String>()) } }
    val texts = remember(questions) { List(questions.size) { mutableStateOf("") } }
    val ready = buildAnswers(questions, selections.map { it.value }, texts.map { it.value }) != null

    Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
        questions.forEachIndexed { index, question ->
            Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                Text(
                    question.header.ifBlank { "问题 ${index + 1}" },
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.primary,
                    fontWeight = FontWeight.SemiBold,
                )
                Text(question.question, style = MaterialTheme.typography.bodyMedium)
                question.options.forEach { option ->
                    val checked = option.label in selections[index].value
                    Row(
                        verticalAlignment = Alignment.CenterVertically,
                        modifier = Modifier
                            .fillMaxWidth()
                            .clip(RoundedCornerShape(8.dp))
                            .clickable {
                                selections[index].value = if (question.multiSelect) {
                                    if (checked) selections[index].value - option.label else selections[index].value + option.label
                                } else {
                                    setOf(option.label)
                                }
                                if (!question.multiSelect) texts[index].value = ""
                            }
                            .padding(vertical = 2.dp),
                    ) {
                        if (question.multiSelect) Checkbox(checked = checked, onCheckedChange = null)
                        else RadioButton(selected = checked, onClick = null)
                        Column {
                            Text(option.label, style = MaterialTheme.typography.bodyMedium)
                            if (option.description.isNotBlank()) {
                                Text(
                                    option.description,
                                    style = MaterialTheme.typography.bodySmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                )
                            }
                        }
                    }
                }
                // 「其他」自填项：协议约定由 UI 追加，输入即视为作答。
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    if (question.multiSelect) {
                        Checkbox(checked = texts[index].value.isNotBlank(), onCheckedChange = { on ->
                            texts[index].value = if (on) " " else ""
                        })
                    } else {
                        RadioButton(selected = texts[index].value.isNotBlank(), onClick = {
                            selections[index].value = emptySet()
                            texts[index].value = " "
                        })
                    }
                    OutlinedTextField(
                        value = texts[index].value.trim(),
                        onValueChange = {
                            texts[index].value = it
                            if (!question.multiSelect && it.isNotBlank()) selections[index].value = emptySet()
                        },
                        label = { Text("其他") },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
            }
            if (index < questions.lastIndex) HorizontalDivider()
        }
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedButton(onClick = onSkip) { Text("跳过") }
            Button(
                onClick = { buildAnswers(questions, selections.map { it.value }, texts.map { it.value })?.let(onConfirm) },
                enabled = ready,
            ) { Text("提交回答") }
        }
    }
}
