package com.hwj.easyagent.mobile.backend

/** A final answer is only promoted when it follows all work and the turn has ended. */
data class ProcessTurn(val user: ChatMessage?, val process: List<ChatMessage>, val answer: ChatMessage?, val open: Boolean)

fun processTurns(messages: List<ChatMessage>, running: Boolean, incomplete: Boolean = false): List<ProcessTurn> {
    val groups = mutableListOf<MutableList<ChatMessage>>()
    messages.forEach { message ->
        if (message.user || groups.isEmpty()) groups.add(mutableListOf())
        groups.last().add(message)
    }
    return groups.mapIndexed { index, group ->
        val user = group.firstOrNull()?.takeIf { it.user }
        val body = if (user == null) group else group.drop(1)
        val active = running && index == groups.lastIndex
        val failed = (incomplete && index == groups.lastIndex) || body.any { it.kind == "error" || it.status in setOf("失败", "已拒绝", "未完成") }
        val answer = body.lastOrNull()?.takeIf { !active && !failed && it.kind.isEmpty() && it.text.isNotBlank() }
        ProcessTurn(user, if (answer == null) body else body.dropLast(1), answer, active || failed || answer == null)
    }
}
