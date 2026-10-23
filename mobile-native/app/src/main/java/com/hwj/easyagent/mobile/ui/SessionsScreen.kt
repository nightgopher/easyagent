package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.PushPin
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.HostConnections
import com.hwj.easyagent.mobile.backend.LinkStatus
import com.hwj.easyagent.mobile.backend.SessionSummary
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SessionsScreen(
    client: AgentClient,
    connections: HostConnections,
    onOpen: (String) -> Unit,
    onSelectHost: (String) -> Unit,
    onManageHosts: () -> Unit,
    onUpdates: () -> Unit,
) {
    val hosts by connections.state.collectAsStateWithLifecycle()
    val sessions by client.sessions.collectAsStateWithLifecycle()
    val loading by client.sessionsLoading.collectAsStateWithLifecycle()
    val link by client.link.collectAsStateWithLifecycle()
    val error by client.error.collectAsStateWithLifecycle()
    val snackbar = remember { SnackbarHostState() }
    var showArchived by rememberSaveable { mutableStateOf(false) }
    var menuOpen by remember { mutableStateOf(false) }
    var hostMenuOpen by remember { mutableStateOf(false) }
    val host = hosts.hosts.firstOrNull { it.id == client.config.id } ?: client.config
    var renaming by remember { mutableStateOf<SessionSummary?>(null) }
    var deleting by remember { mutableStateOf<SessionSummary?>(null) }

    LaunchedEffect(error) {
        error?.let {
            snackbar.showSnackbar(it)
            client.error.value = null
        }
    }

    val active = sessions.filter { !it.archived }
    val archived = sessions.filter { it.archived }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Column {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Text("EasyAgent")
                            IconButton(onClick = { hostMenuOpen = true }) { Icon(Icons.Filled.ExpandMore, contentDescription = "切换运行主机") }
                            DropdownMenu(expanded = hostMenuOpen, onDismissRequest = { hostMenuOpen = false }) {
                                hosts.hosts.forEach { saved ->
                                    DropdownMenuItem(
                                        text = {
                                            Column {
                                                Text(saved.label + if (saved.id == hosts.activeId) " · 当前" else "")
                                                Text(hostStatus(connections.clientFor(saved.id)), style = MaterialTheme.typography.labelSmall)
                                            }
                                        },
                                        onClick = { hostMenuOpen = false; onSelectHost(saved.id) },
                                    )
                                }
                                HorizontalDivider()
                                DropdownMenuItem(text = { Text("管理运行主机") }, onClick = { hostMenuOpen = false; onManageHosts() })
                            }
                        }
                        Text(
                            host.label + " · " + linkLabel(link),
                            style = MaterialTheme.typography.labelSmall,
                            color = if (link == LinkStatus.CONNECTED) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.error,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                        )
                    }
                },
                actions = {
                    IconButton(onClick = { client.refreshSessions() }) { Icon(Icons.Filled.Refresh, contentDescription = "刷新会话") }
                    IconButton(onClick = { menuOpen = true }) { Icon(Icons.Filled.MoreVert, contentDescription = "更多") }
                    DropdownMenu(expanded = menuOpen, onDismissRequest = { menuOpen = false }) {
                        DropdownMenuItem(
                            text = { Text(if (showArchived) "隐藏已归档" else "显示已归档（${archived.size}）") },
                            onClick = { showArchived = !showArchived; menuOpen = false },
                        )
                        DropdownMenuItem(text = { Text("管理运行主机") }, onClick = { menuOpen = false; onManageHosts() })
                        DropdownMenuItem(text = { Text("检查更新") }, onClick = { menuOpen = false; onUpdates() })
                    }
                },
            )
        },
        floatingActionButton = {
            ExtendedFloatingActionButton(
                onClick = { client.createSession(onOpen) },
                icon = { Icon(Icons.Filled.Add, contentDescription = null) },
                text = { Text("新会话") },
            )
        },
        snackbarHost = { SnackbarHost(snackbar) },
    ) { padding ->
        PullToRefreshBox(
            isRefreshing = loading,
            onRefresh = { client.refreshSessions() },
            modifier = Modifier.padding(padding).fillMaxSize(),
        ) {
            LazyColumn(modifier = Modifier.fillMaxSize(), contentPadding = PaddingValues(bottom = 96.dp)) {
                if (active.isEmpty() && !loading) {
                    item {
                        Box(modifier = Modifier.fillMaxWidth().padding(32.dp), contentAlignment = Alignment.Center) {
                            Text(
                                if (archived.isEmpty()) "还没有会话，点击右下角新建。" else "没有进行中的会话。",
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                    }
                }
                items(active, key = { it.id }) { session ->
                    SessionRow(
                        session = session,
                        onOpen = { onOpen(session.id) },
                        onRename = { renaming = session },
                        onDelete = { deleting = session },
                        onTogglePinned = { client.patchSession(session.id, buildJsonObject { put("pinned", !session.pinned) }) },
                        onToggleArchived = { client.patchSession(session.id, buildJsonObject { put("archived", !session.archived) }) },
                    )
                }
                if (showArchived && archived.isNotEmpty()) {
                    item {
                        HorizontalDivider()
                        Text(
                            "已归档",
                            style = MaterialTheme.typography.labelLarge,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                        )
                    }
                    items(archived, key = { it.id }) { session ->
                        SessionRow(
                            session = session,
                            onOpen = { onOpen(session.id) },
                            onRename = { renaming = session },
                            onDelete = { deleting = session },
                            onTogglePinned = { client.patchSession(session.id, buildJsonObject { put("pinned", !session.pinned) }) },
                            onToggleArchived = { client.patchSession(session.id, buildJsonObject { put("archived", !session.archived) }) },
                        )
                    }
                }
            }
        }
    }

    renaming?.let { session ->
        RenameDialog(
            initial = session.displayTitle,
            onDismiss = { renaming = null },
            onConfirm = { title ->
                client.patchSession(session.id, buildJsonObject { put("title", title) })
                renaming = null
            },
        )
    }
    deleting?.let { session ->
        AlertDialog(
            onDismissRequest = { deleting = null },
            title = { Text("删除会话") },
            text = { Text("将从运行主机删除「${session.displayTitle}」及其历史记录，无法恢复。") },
            confirmButton = {
                Button(onClick = { client.deleteSession(session.id); deleting = null }) { Text("删除") }
            },
            dismissButton = { TextButton(onClick = { deleting = null }) { Text("取消") } },
        )
    }
}

@Composable
private fun SessionRow(
    session: SessionSummary,
    onOpen: () -> Unit,
    onRename: () -> Unit,
    onDelete: () -> Unit,
    onTogglePinned: () -> Unit,
    onToggleArchived: () -> Unit,
) {
    var menu by remember { mutableStateOf(false) }
    val pinnedIcon: (@Composable () -> Unit)? = if (session.pinned) {
        { Icon(Icons.Filled.PushPin, contentDescription = "已置顶", tint = MaterialTheme.colorScheme.primary) }
    } else {
        null
    }
    ListItem(
        modifier = Modifier.clickable(onClick = onOpen),
        headlineContent = { Text(session.displayTitle, maxLines = 1, overflow = TextOverflow.Ellipsis) },
        supportingContent = {
            val parts = listOfNotNull(
                formatUpdated(session.updatedAt).takeIf { it.isNotEmpty() },
                session.workspace.substringAfterLast('/').takeIf { it.isNotEmpty() },
            )
            if (parts.isNotEmpty()) Text(parts.joinToString(" · "), maxLines = 1, overflow = TextOverflow.Ellipsis)
        },
        leadingContent = pinnedIcon,
        trailingContent = {
            IconButton(onClick = { menu = true }) { Icon(Icons.Filled.MoreVert, contentDescription = "会话操作") }
            DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                DropdownMenuItem(text = { Text("重命名") }, onClick = { menu = false; onRename() })
                DropdownMenuItem(text = { Text(if (session.pinned) "取消置顶" else "置顶") }, onClick = { menu = false; onTogglePinned() })
                DropdownMenuItem(text = { Text(if (session.archived) "取消归档" else "归档") }, onClick = { menu = false; onToggleArchived() })
                DropdownMenuItem(text = { Text("删除", color = MaterialTheme.colorScheme.error) }, onClick = { menu = false; onDelete() })
            }
        },
    )
}

@Composable
private fun RenameDialog(initial: String, onDismiss: () -> Unit, onConfirm: (String) -> Unit) {
    var title by remember { mutableStateOf(initial) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("重命名会话") },
        text = { OutlinedTextField(value = title, onValueChange = { title = it }, singleLine = true, modifier = Modifier.fillMaxWidth()) },
        confirmButton = {
            Button(onClick = { onConfirm(title.trim()) }, enabled = title.isNotBlank()) { Text("保存") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("取消") } },
    )
}

internal fun linkLabel(link: LinkStatus): String = when (link) {
    LinkStatus.CONNECTED -> "已连接"
    LinkStatus.CONNECTING -> "连接中"
    LinkStatus.DISCONNECTED -> "已断开，正在重连"
}

private val updatedFormatter: DateTimeFormatter = DateTimeFormatter.ofPattern("MM-dd HH:mm")

fun formatUpdated(raw: String): String {
    if (raw.isEmpty()) return ""
    return runCatching { OffsetDateTime.parse(raw).atZoneSameInstant(ZoneId.systemDefault()).format(updatedFormatter) }
        .getOrDefault(raw.take(16))
}
