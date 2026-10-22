package com.hwj.easyagent.mobile.backend

/** Something worth telling the user about while they are not looking at the session. */
sealed class Alert {
    abstract val sessionId: String

    /** A tool waits for approval; the server rejects it if nobody answers in time. */
    data class Approval(
        override val sessionId: String,
        val confirmationId: String,
        val toolName: String,
        val description: String,
    ) : Alert()

    /** A run that was running ended: completed, failed or interrupted. */
    data class RunEnded(override val sessionId: String, val state: String, val error: String) : Alert()
}

/** What [alertsFor] compares: the part of a session's state seen before an event. */
data class AlertState(val running: Boolean, val confirmationIds: Set<String>)

fun ChatSession.alertState() = AlertState(running, confirmations.mapTo(HashSet()) { it.id })

/**
 * Compares a session before and after an event was applied. Approvals already
 * known before (for example re-sent in a snapshot after reconnecting) and runs
 * that were not running do not alert again.
 */
fun alertsFor(before: AlertState, after: ChatSession): List<Alert> {
    val alerts = ArrayList<Alert>()
    for (c in after.confirmations) {
        if (c.id !in before.confirmationIds) alerts += Alert.Approval(after.id, c.id, c.toolName, c.description)
    }
    val state = after.run?.state ?: ""
    if (before.running && !after.running && state in setOf("completed", "failed", "interrupted")) {
        alerts += Alert.RunEnded(after.id, state, after.run?.error ?: "")
    }
    return alerts
}
