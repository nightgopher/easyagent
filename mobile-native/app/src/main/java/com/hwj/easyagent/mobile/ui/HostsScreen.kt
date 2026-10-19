package com.hwj.easyagent.mobile.ui

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedCard
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.HostConfig
import com.hwj.easyagent.mobile.backend.HostConnections
import kotlinx.coroutines.launch

@Composable
internal fun hostStatus(client: AgentClient?): String {
    if (client == null) return "令牌无法读取，请编辑连接"
    val link by client.link.collectAsStateWithLifecycle()
    return linkLabel(link)
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun HostsScreen(
    connections: HostConnections,
    canGoBack: Boolean,
    onBack: () -> Unit,
    onAdd: () -> Unit,
    onEdit: (HostConfig) -> Unit,
    onSelect: (String) -> Unit,
) {
    val state by connections.state.collectAsStateWithLifecycle()
    var deleting by remember { mutableStateOf<HostConfig?>(null) }
    val snackbar = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    if (canGoBack) BackHandler(onBack = onBack)

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("运行主机") },
                navigationIcon = {
                    if (canGoBack) IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "返回")
                    }
                },
                actions = { IconButton(onClick = onAdd) { Icon(Icons.Filled.Add, contentDescription = "添加主机") } },
            )
        },
        snackbarHost = { SnackbarHost(snackbar) },
    ) { padding ->
        LazyColumn(
            modifier = Modifier.padding(padding),
            contentPadding = PaddingValues(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            item {
                Text("会话、草稿与连接状态按主机独立保存，切换不会断开其他主机。", color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            items(state.hosts, key = { it.id }) { host ->
                OutlinedCard(modifier = Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text(host.label, style = MaterialTheme.typography.titleMedium)
                        if (host.name.isNotBlank()) Text(host.url, style = MaterialTheme.typography.bodySmall)
                        Text(hostStatus(connections.clientFor(host.id)), style = MaterialTheme.typography.labelMedium)
                        Row {
                            TextButton(onClick = { onSelect(host.id) }, enabled = host.id != state.activeId) {
                                Text(if (host.id == state.activeId) "当前主机" else "切换")
                            }
                            TextButton(onClick = { onEdit(host) }) { Text("编辑") }
                            IconButton(onClick = { deleting = host }) { Icon(Icons.Filled.Delete, contentDescription = "移除 ${host.label}") }
                        }
                    }
                }
            }
            item { Button(onClick = onAdd, modifier = Modifier.fillMaxWidth()) { Text("添加运行主机") } }
        }
    }
    deleting?.let { host ->
        AlertDialog(
            onDismissRequest = { deleting = null },
            title = { Text("移除 ${host.label}？") },
            text = { Text("移除此手机保存的连接、令牌及本地草稿，服务器上的会话不受影响。") },
            confirmButton = {
                TextButton(onClick = {
                    try {
                        connections.remove(host.id)
                        deleting = null
                    } catch (_: Exception) {
                        scope.launch { snackbar.showSnackbar("无法移除主机连接，请重试") }
                    }
                }) { Text("移除") }
            },
            dismissButton = { TextButton(onClick = { deleting = null }) { Text("取消") } },
        )
    }
}
