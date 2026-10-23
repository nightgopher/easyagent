package com.hwj.easyagent.mobile

import android.content.Intent
import android.content.ActivityNotFoundException
import android.net.Uri
import android.os.Bundle
import android.provider.Settings
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.ComponentActivity
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.hwj.easyagent.mobile.ui.ChatScreen
import com.hwj.easyagent.mobile.ui.ConnectionScreen
import com.hwj.easyagent.mobile.ui.EasyAgentTheme
import com.hwj.easyagent.mobile.ui.HostsScreen
import com.hwj.easyagent.mobile.ui.SessionsScreen
import com.hwj.easyagent.mobile.ui.UpdateDialog
import com.hwj.easyagent.mobile.updates.updateInstallIntent

class MainActivity : ComponentActivity() {
    private val updater get() = (application as EasyAgentApp).updater
    private val installPermission = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) {
        if (updater.state.value.file == null) updater.open() // Recover a cached download after process recreation.
        else if (packageManager.canRequestPackageInstalls()) installUpdate()
        else updater.installationError("尚未允许安装更新，可点击安装更新后重新授权")
    }

    private fun installUpdate() {
        if (!packageManager.canRequestPackageInstalls()) {
            try {
                installPermission.launch(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")))
            } catch (_: ActivityNotFoundException) { updater.installationError("无法打开安装授权设置，请在系统设置中允许 EasyAgent 安装应用") }
            return
        }
        updater.install { file ->
            try { startActivity(updateInstallIntent(this, file)) }
            catch (_: ActivityNotFoundException) { updater.installationError("系统无法打开安装器，请稍后重试") }
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        handleOpen(intent)
        setContent {
            EasyAgentTheme {
                Root(application as EasyAgentApp, ::installUpdate)
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleOpen(intent)
    }

    override fun onStart() {
        super.onStart()
        AppVisibility.foreground = true
        (application as EasyAgentApp).connections.resume()
        updater.foreground()
    }

    override fun onStop() {
        AppVisibility.foreground = false
        super.onStop()
    }

    private fun handleOpen(intent: Intent?) {
        val host = intent?.getStringExtra(Notifier.EXTRA_HOST) ?: return
        val session = intent.getStringExtra(Notifier.EXTRA_SESSION) ?: return
        (application as EasyAgentApp).pendingOpen.value = OpenRequest(host, session)
    }
}

@Composable
private fun Root(app: EasyAgentApp, onInstall: () -> Unit) {
    val connections = app.connections
    val current by connections.client.collectAsStateWithLifecycle()
    val state by connections.state.collectAsStateWithLifecycle()
    var editing by rememberSaveable { mutableStateOf(false) }
    var editingId by rememberSaveable { mutableStateOf<String?>(null) }
    var managing by rememberSaveable { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val pendingOpen by app.pendingOpen.collectAsStateWithLifecycle()

    fun select(id: String) {
        try {
            connections.select(id)
            managing = false
        } catch (_: Exception) {
            error = "无法切换运行主机，请重试"
        }
    }

    // A notification names a host: switch to it first, then its chat opens below.
    LaunchedEffect(pendingOpen) {
        val request = pendingOpen ?: return@LaunchedEffect
        if (current?.config?.id != request.host && state.hosts.any { it.id == request.host }) select(request.host)
    }

    when {
        state.hosts.isEmpty() || editing -> key(editingId) {
            ConnectionScreen(
                initial = state.hosts.firstOrNull { it.id == editingId },
                canCancel = state.hosts.isNotEmpty(),
                onUpdates = app.updater::open,
                onCancel = { editing = false },
                onSave = { config ->
                    connections.save(config)
                    editing = false
                    managing = false
                },
            )
        }
        managing || current == null -> HostsScreen(
            connections = connections,
            canGoBack = current != null,
            onBack = { managing = false },
            onAdd = { editingId = null; editing = true },
            onEdit = { editingId = it.id; editing = true },
            onSelect = ::select,
            onUpdates = app.updater::open,
        )
        else -> key(current) {
            val client = requireNotNull(current)
            var chatId by rememberSaveable { mutableStateOf("") }
            LaunchedEffect(pendingOpen) {
                val request = pendingOpen ?: return@LaunchedEffect
                if (request.host == client.config.id) {
                    chatId = request.session
                    app.pendingOpen.value = null
                }
            }
            DisposableEffect(chatId) {
                AppVisibility.openChat = if (chatId.isEmpty()) null else "${client.config.id}/$chatId"
                onDispose { AppVisibility.openChat = null }
            }
            if (chatId.isNotEmpty()) {
                ChatScreen(client = client, sessionId = chatId, onBack = { chatId = "" })
            } else {
                SessionsScreen(
                    client = client,
                    connections = connections,
                    onOpen = { chatId = it },
                    onSelectHost = ::select,
                    onManageHosts = { managing = true },
                    onUpdates = app.updater::open,
                )
            }
        }
    }
    UpdateDialog(app.updater, onInstall)
    error?.let { message ->
        AlertDialog(
            onDismissRequest = { error = null },
            title = { Text("主机连接") },
            text = { Text(message) },
            confirmButton = { TextButton(onClick = { error = null }) { Text("确定") } },
        )
    }
}
