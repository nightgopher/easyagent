package com.hwj.easyagent.mobile

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat

/**
 * 后台保活前台服务：App 退到后台后维持进程与 WebSocket，审批与任务结果的通知才能送达。
 * 连接由 Application 管辖，这里只负责让系统不回收进程；系统杀死后靠 START_STICKY 拉起，
 * 进程重建时 Application 会自动恢复各主机连接。可在会话列表「更多」菜单关闭。
 */
class KeepAliveService : Service() {
    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        started = true
        getSystemService(NotificationManager::class.java).createNotificationChannel(
            NotificationChannel(CHANNEL, "后台保持连接", NotificationManager.IMPORTANCE_MIN).apply {
                description = "保持与运行主机的连接，及时接收审批与任务结果通知；可在应用内关闭"
            },
        )
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val notification: Notification = NotificationCompat.Builder(this, CHANNEL)
            .setContentTitle("EasyAgent 保持连接")
            .setContentText("后台维持与运行主机的连接，及时接收审批与任务结果")
            .setSmallIcon(R.drawable.ic_notification)
            .setOngoing(true)
            .setPriority(NotificationCompat.PRIORITY_MIN)
            .build()
        // dataSync 类型要求 API 29+；更早版本由 ServiceCompat 忽略类型参数。
        ServiceCompat.startForeground(
            this,
            NOTIFICATION_ID,
            notification,
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC else 0,
        )
        return START_STICKY
    }

    override fun onDestroy() {
        started = false
        super.onDestroy()
    }

    companion object {
        private const val CHANNEL = "keepalive"
        private const val NOTIFICATION_ID = 41

        @Volatile
        var started = false
            private set
    }
}

/** 按「开关开启且已配置主机」启停前台服务；应在前台状态调用，避开后台启动 FGS 的系统限制。 */
fun syncKeepAlive(context: Context) {
    val app = context.applicationContext as EasyAgentApp
    val want = app.settings.keepAlive && app.connections.state.value.hosts.isNotEmpty()
    if (want && !KeepAliveService.started) {
        ContextCompat.startForegroundService(context, Intent(context, KeepAliveService::class.java))
    } else if (!want && KeepAliveService.started) {
        context.stopService(Intent(context, KeepAliveService::class.java))
    }
}
