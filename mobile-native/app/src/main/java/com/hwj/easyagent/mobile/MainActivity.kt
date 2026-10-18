package com.hwj.easyagent.mobile

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.hwj.easyagent.mobile.ui.ChatScreen
import com.hwj.easyagent.mobile.ui.ConnectionScreen
import com.hwj.easyagent.mobile.ui.EasyAgentTheme
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
        (application as EasyAgentApp).client.value?.resume()
    }
}

@Composable
private fun Root(app: EasyAgentApp) {
    val client by app.client.collectAsStateWithLifecycle()
    var chatId by rememberSaveable { mutableStateOf("") }
    var editing by rememberSaveable { mutableStateOf(false) }
    val current = client
    when {
        current == null || editing -> {
            val initial = remember(current) { app.settings.load() }
            ConnectionScreen(
                initial = initial,
                canCancel = current != null,
                onCancel = { editing = false },
                onConnected = { config ->
                    app.connect(config)
                    editing = false
                    chatId = ""
                },
            )
        }
        chatId.isNotEmpty() -> ChatScreen(client = current, sessionId = chatId, onBack = { chatId = "" })
        else -> SessionsScreen(
            client = current,
            onOpen = { chatId = it },
            onEditConnection = { editing = true },
            onDisconnect = {
                app.disconnect()
                chatId = ""
            },
        )
    }
}
