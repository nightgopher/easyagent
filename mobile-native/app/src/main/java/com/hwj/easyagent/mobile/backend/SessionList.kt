package com.hwj.easyagent.mobile.backend

import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/** Sessions whose title or project contains every word of [query], case-insensitively. */
fun filterSessions(sessions: List<SessionSummary>, query: String): List<SessionSummary> {
    val words = query.trim().lowercase().split(Regex("\\s+")).filter { it.isNotEmpty() }
    if (words.isEmpty()) return sessions
    return sessions.filter { s ->
        val haystack = (s.displayTitle + " " + s.workspace).lowercase()
        words.all { it in haystack }
    }
}

/** A list section: pinned sessions first, then one section per project. */
data class SessionGroup(val title: String, val sessions: List<SessionSummary>)

/**
 * Groups by project (workspace folder name), most recently active project
 * first. With a single project there are no headings: one untitled group.
 */
fun groupSessions(sessions: List<SessionSummary>): List<SessionGroup> {
    val pinned = sessions.filter { it.pinned }.sortedByDescending { it.lastActive }
    val rest = sessions.filter { !it.pinned }
    val byProject = rest.groupBy { project(it) }
        .map { (name, list) -> SessionGroup(name, list.sortedByDescending { it.lastActive }) }
        .sortedByDescending { group -> group.sessions.maxOf { it.lastActive } }
    val groups = ArrayList<SessionGroup>()
    if (pinned.isNotEmpty()) groups += SessionGroup("置顶", pinned)
    if (byProject.size == 1 && pinned.isEmpty()) return listOf(byProject[0].copy(title = ""))
    groups += byProject
    return groups
}

private fun project(s: SessionSummary): String =
    s.workspace.trimEnd('/').substringAfterLast('/').ifEmpty { "未指定项目" }

private val dateFormat = DateTimeFormatter.ofPattern("M月d日")

/** 刚刚 / N 分钟前 / N 小时前 / 昨天 / M月d日, in the device's time zone. */
fun relativeTime(epochSeconds: Long, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    if (epochSeconds <= 0) return ""
    val then = Instant.ofEpochSecond(epochSeconds)
    val seconds = now.epochSecond - epochSeconds
    val day = then.atZone(zone).toLocalDate()
    val today = now.atZone(zone).toLocalDate()
    return when {
        seconds < 60 -> "刚刚"
        seconds < 3600 -> "${seconds / 60} 分钟前"
        day == today -> "${seconds / 3600} 小时前"
        day == today.minusDays(1) -> "昨天"
        else -> then.atZone(zone).format(dateFormat)
    }
}
