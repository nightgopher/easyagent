package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp

/** The session home still has a useful destination before a host is paired. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun EmptyHomeScreen(hasSavedHosts: Boolean, onConnect: () -> Unit, onUpdates: () -> Unit) {
    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("EasyAgent") },
                actions = { TextButton(onClick = onUpdates) { Text("检查更新") } },
            )
        },
    ) { padding ->
        Box(Modifier.padding(padding).fillMaxSize()) {
            Column(
                modifier = Modifier.align(Alignment.Center).fillMaxWidth().verticalScroll(rememberScrollState()).padding(24.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.spacedBy(16.dp),
            ) {
                Icon(Icons.Filled.Computer, contentDescription = null, modifier = Modifier.size(48.dp), tint = MaterialTheme.colorScheme.primary)
                Text(if (hasSavedHosts) "连接需要修复" else "还没有连接电脑", style = MaterialTheme.typography.titleLarge, textAlign = TextAlign.Center)
                Text(
                    if (hasSavedHosts) "当前电脑的凭据无法读取，请在主机管理中重新配对或编辑连接。" else "连接电脑后，可以在手机查看会话、发送消息和处理审批。",
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    style = MaterialTheme.typography.bodyMedium,
                    textAlign = TextAlign.Center,
                )
                Button(onClick = onConnect) { Text(if (hasSavedHosts) "管理运行主机" else "连接电脑") }
            }
        }
    }
}
