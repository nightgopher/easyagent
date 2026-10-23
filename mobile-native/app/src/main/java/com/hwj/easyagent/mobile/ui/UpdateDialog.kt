package com.hwj.easyagent.mobile.ui

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.selection.toggleable
import androidx.compose.material3.*
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.hwj.easyagent.mobile.updates.AppUpdater
import java.util.Locale

@Composable
fun UpdateDialog(updater: AppUpdater, onInstall: () -> Unit) {
    val state by updater.state.collectAsStateWithLifecycle()
    if (!state.visible) return
    AlertDialog(
        onDismissRequest = updater::dismiss,
        title = { Text("应用更新") },
        text = {
            Column(Modifier.heightIn(max = 420.dp).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Text("当前版本 ${updater.installed.name}")
                Row(Modifier.fillMaxWidth().toggleable(value = state.automatic, role = Role.Checkbox, onValueChange = updater::setAutomatic), verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(checked = state.automatic, onCheckedChange = null)
                    Text("自动检查更新")
                }
                if (state.checking) {
                    LinearProgressIndicator(Modifier.fillMaxWidth())
                    Text("正在检查更新…")
                }
                val update = state.update
                if (update != null) {
                    Text("发现新版本 ${update.version}", style = MaterialTheme.typography.titleMedium)
                    Text("安装包 ${String.format(Locale.ROOT, "%.1f", update.size / 1048576.0)} MB")
                    if (update.notes.isNotBlank()) Text(update.notes, style = MaterialTheme.typography.bodySmall)
                    state.progress?.let { percent ->
                        LinearProgressIndicator(progress = { percent / 100f }, modifier = Modifier.fillMaxWidth())
                        Text(if (percent == 100) "正在校验安装包…" else "正在下载 $percent%")
                    }
                    if (state.file != null) Text("下载完成，点击安装后由系统确认。安装会重启应用，请先处理未发送的草稿；已保存的主机配置与凭据会保留。")
                    if (state.verifying) Text("正在校验安装包…")
                } else if (state.checked && !state.checking && state.error == null) Text("已是最新版本")
                state.error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
            }
        },
        confirmButton = {
            when {
                state.progress != null -> TextButton(onClick = updater::cancelDownload) { Text("取消下载") }
                state.file != null -> TextButton(enabled = !state.busy, onClick = onInstall) { Text("安装更新") }
                state.update != null -> TextButton(enabled = !state.busy, onClick = updater::download) { Text(if (state.error == null) "下载更新" else "重新下载") }
                else -> TextButton(enabled = !state.busy, onClick = { updater.check() }) { Text("检查更新") }
            }
        },
        dismissButton = { TextButton(onClick = updater::dismiss) { Text("稍后") } },
    )
}
