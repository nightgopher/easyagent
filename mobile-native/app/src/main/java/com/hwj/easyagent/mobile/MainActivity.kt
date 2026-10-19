package com.hwj.easyagent.mobile

import android.os.Bundle
import androidx.activity.ComponentActivity
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
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        setContent {
            EasyAgentTheme {
                Root(application as EasyAgentApp)
            }
        }
    }

    override fun onStart() {
        super.onStart()
        (application as EasyAgentApp).connections.resume()
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

    fun select(id: String) {
        try {
            connections.select(id)
            managing = false
        } catch (_: Exception) {
            error = "无法切换运行主机，请重试"
        }
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
