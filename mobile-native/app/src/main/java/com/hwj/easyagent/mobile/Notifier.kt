package com.hwj.easyagent.mobile

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.Alert

/** Whether the app is on screen and which chat ("hostId/sessionId") is open. */
object AppVisibility {
    @Volatile var foreground = false
    @Volatile var openChat: String? = null
}

/**
 * Posts notifications for approvals and finished runs while the user is not
 * looking at that session. They arrive only while the app process and its
 * WebSocket are alive; nothing is delivered after Android stops the app.
 */
class Notifier(private val context: Context) {
    init {
        val manager = context.getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(
            NotificationChannel(CHANNEL_APPROVALS, "待审批", NotificationManager.IMPORTANCE_HIGH).apply {
                description = "工具需要你批准后才会执行；无人处理时服务端会自动拒绝"
            },
        )
        manager.createNotificationChannel(
            NotificationChannel(CHANNEL_RUNS, "任务结果", NotificationManager.IMPORTANCE_DEFAULT).apply {
                description = "任务完成、失败或中断"
            },
        )
    }

    fun post(client: AgentClient, alert: Alert) {
        val host = client.config.id
        if (AppVisibility.foreground && AppVisibility.openChat == "$host/${alert.sessionId}") return
        val title = client.titleOf(alert.sessionId)
        val builder = when (alert) {
            is Alert.Approval -> {
                // 问答类确认无法在通知里作答（须提交 answers）：只给「跳过」，点通知本体去回答。
                val question = alert.toolName == "ask_user_question"
                NotificationCompat.Builder(context, CHANNEL_APPROVALS)
                    .setContentTitle(if (question) "待回答 · $title" else "待审批 · $title")
                    .setContentText(alert.description.ifBlank { alert.toolName })
                    .setStyle(NotificationCompat.BigTextStyle().bigText(alert.description.ifBlank { alert.toolName }))
                    .setPriority(NotificationCompat.PRIORITY_HIGH)
                    .setCategory(NotificationCompat.CATEGORY_REMINDER)
                    .apply {
                        if (question) addAction(0, "跳过", actionIntent(host, alert, approved = false))
                        else {
                            addAction(0, "批准", actionIntent(host, alert, approved = true))
                            addAction(0, "拒绝", actionIntent(host, alert, approved = false))
                        }
                    }
            }
            is Alert.RunEnded -> NotificationCompat.Builder(context, CHANNEL_RUNS)
                .setContentTitle(
                    when (alert.state) {
                        "completed" -> "任务完成 · $title"
                        "failed" -> "任务失败 · $title"
                        else -> "任务已中断 · $title"
                    },
                )
                .setContentText(alert.error.ifBlank { "点按查看结果" })
        }
        builder.setSmallIcon(R.drawable.ic_notification)
            .setAutoCancel(true)
            .setContentIntent(openIntent(host, alert.sessionId))
        // Android 13+ asks the user first; without the grant nothing is posted.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            return
        }
        NotificationManagerCompat.from(context).notify(notificationId(host, alert), builder.build())
    }

    private fun openIntent(host: String, sessionId: String): PendingIntent {
        val intent = Intent(context, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)
            .putExtra(EXTRA_HOST, host)
            .putExtra(EXTRA_SESSION, sessionId)
        return PendingIntent.getActivity(context, (host + sessionId).hashCode(), intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
    }

    private fun actionIntent(host: String, alert: Alert.Approval, approved: Boolean): PendingIntent {
        val intent = Intent(context, ApprovalActionReceiver::class.java)
            .putExtra(EXTRA_HOST, host)
            .putExtra(EXTRA_SESSION, alert.sessionId)
            .putExtra(EXTRA_CONFIRMATION, alert.confirmationId)
            .putExtra(EXTRA_APPROVED, approved)
            .putExtra(EXTRA_NOTIFICATION, notificationId(host, alert))
        val code = (host + alert.confirmationId + approved).hashCode()
        return PendingIntent.getBroadcast(context, code, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
    }

    companion object {
        const val CHANNEL_APPROVALS = "approvals"
        const val CHANNEL_RUNS = "runs"
        const val EXTRA_HOST = "host"
        const val EXTRA_SESSION = "session"
        const val EXTRA_CONFIRMATION = "confirmation"
        const val EXTRA_APPROVED = "approved"
        const val EXTRA_NOTIFICATION = "notification"

        fun notificationId(host: String, alert: Alert): Int = when (alert) {
            is Alert.Approval -> "$host/approval/${alert.confirmationId}".hashCode()
            is Alert.RunEnded -> "$host/run/${alert.sessionId}".hashCode()
        }
    }
}

/** Answers an approval straight from its notification. */
class ApprovalActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val app = context.applicationContext as EasyAgentApp
        val host = intent.getStringExtra(Notifier.EXTRA_HOST) ?: return
        val session = intent.getStringExtra(Notifier.EXTRA_SESSION) ?: return
        val confirmation = intent.getStringExtra(Notifier.EXTRA_CONFIRMATION) ?: return
        val approved = intent.getBooleanExtra(Notifier.EXTRA_APPROVED, false)
        app.connections.clientFor(host)?.confirmById(session, confirmation, approved)
        NotificationManagerCompat.from(context).cancel(intent.getIntExtra(Notifier.EXTRA_NOTIFICATION, 0))
    }
}
