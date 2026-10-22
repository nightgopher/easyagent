package com.hwj.easyagent.mobile

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.core.content.ContextCompat
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

class MainActivity : ComponentActivity() {
    private val notificationPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        handleOpen(intent)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
        setContent {
            EasyAgentTheme {
                Root(application as EasyAgentApp)
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
private fun Root(app: EasyAgentApp) {
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
                )
            }
        }
    }
    error?.let { message ->
        AlertDialog(
            onDismissRequest = { error = null },
            title = { Text("主机连接") },
            text = { Text(message) },
            confirmButton = { TextButton(onClick = { error = null }) { Text("确定") } },
        )
    }
}
