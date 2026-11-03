package com.hwj.easyagent.mobile.ui

import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Visibility
import androidx.compose.material.icons.filled.VisibilityOff
import androidx.compose.material3.Button
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import com.hwj.easyagent.mobile.backend.ApiClient
import com.hwj.easyagent.mobile.backend.ApiException
import com.hwj.easyagent.mobile.backend.HostConfig
import com.hwj.easyagent.mobile.backend.RelayPairCode
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import com.hwj.easyagent.mobile.backend.getString
import com.hwj.easyagent.mobile.backend.obj
import kotlinx.coroutines.launch
import java.util.UUID

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ConnectionScreen(
    initial: HostConfig?,
    canCancel: Boolean,
    onCancel: () -> Unit,
    onSave: (HostConfig) -> Unit,
    onUpdates: () -> Unit,
    claim: suspend (RelayPairCode) -> HostConfig = { it.claim(android.os.Build.MODEL) },
) {
    val id = rememberSaveable { initial?.id ?: UUID.randomUUID().toString() }
    var name by rememberSaveable { mutableStateOf(initial?.name ?: "") }
    var url by rememberSaveable { mutableStateOf(initial?.url ?: "") }
    var token by remember { mutableStateOf(initial?.token ?: "") }
    var showToken by rememberSaveable { mutableStateOf(false) }
    var testing by remember { mutableStateOf(false) }
    var result by remember { mutableStateOf<String?>(null) }
    var resultOk by remember { mutableStateOf(false) }
    var pairing by remember { mutableStateOf(false) }
    var pairCode by remember { mutableStateOf("") }
    var scanned by remember { mutableStateOf(false) }
    var paired by remember { mutableStateOf<HostConfig?>(null) }
    var confirmLeave by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    fun cancel() {
        if (testing) return
        if (paired != null) confirmLeave = true else onCancel()
    }
    if (canCancel) BackHandler { cancel() }

    fun config(): HostConfig? = try {
        HostConfig(ApiClient.normalize(url), token.trim(), id, name.trim(), initial?.relay)
    } catch (e: IllegalArgumentException) {
        resultOk = false
        result = e.message
        null
    }

    fun save() {
        val config = config() ?: return
        try {
            onSave(config)
        } catch (e: Exception) {
            resultOk = false
            result = if (e is IllegalArgumentException) e.message else "无法保存主机连接或加密令牌，请重试"
        }
    }

    fun verify() {
        val config = config() ?: return
        testing = true
        result = null
        scope.launch {
            val api = ApiClient(config.url, requireNotNull(config.token), config.relay)
            try {
                val version = api.get("/health").obj().getString("version")
                // /health 不鉴权，再请求会话列表验证令牌。
                api.get("/sessions")
                resultOk = true
                result = "连接成功" + if (version.isNotEmpty()) " · EasyAgent $version" else ""
            } catch (e: ApiException) {
                resultOk = false
                result = e.message
            } finally {
                api.close()
                testing = false
            }
        }
    }

    fun pair(raw: String) {
        if (testing) return
        testing = true
        result = null
        scope.launch {
            try {
                // Keep a successful claim in memory if local secure storage fails;
                // the one-use code must not be submitted again to retry saving.
                val host = paired ?: claim(RelayPairCode.parse(raw)).also { paired = it }
                onSave(host)
                pairCode = ""
            } catch (e: kotlinx.coroutines.CancellationException) { throw e
            } catch (e: Exception) {
                resultOk = false
                result = when {
                    paired != null -> "电脑已完成配对，但手机保存凭据失败，请点“重试保存”"
                    e is ApiException || e is IllegalArgumentException -> e.message
                    else -> "配对失败，请重试；必要时在电脑上撤销设备后重新扫码"
                }
            } finally { testing = false }
        }
    }
    val scanner = rememberLauncherForActivityResult(ScanContract()) { scan ->
        scan.contents?.let {
            if (it != pairCode) paired = null
            pairCode = it
            scanned = true
            pair(it)
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(if (initial == null) "连接运行主机" else "编辑运行主机") },
                actions = { TextButton(onClick = onUpdates, enabled = !testing) { Text("检查更新") } },
                navigationIcon = {
                    if (canCancel) {
                        IconButton(onClick = { cancel() }, enabled = !testing) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "返回") }
                    }
                },
            )
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .padding(padding)
                .fillMaxSize()
                .verticalScroll(rememberScrollState())
                .imePadding()
                .padding(horizontal = 20.dp, vertical = 12.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            if (initial == null) {
                Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    OutlinedButton(onClick = { pairing = true; scanner.launch(ScanOptions().setDesiredBarcodeFormats(ScanOptions.QR_CODE).setPrompt("扫描电脑 EasyAgent 的配对二维码").setBeepEnabled(false).setBarcodeImageEnabled(false).setOrientationLocked(false)) }, enabled = !testing && paired == null) {
                        Text("扫码配对电脑")
                    }
                    TextButton(onClick = {
                        pairing = !pairing
                        if (pairing) { scanned = false; pairCode = "" }
                        result = null
                    }, enabled = !testing && paired == null) { Text(if (pairing) "填写主机地址" else "粘贴配对码") }
                }
            }
            if (pairing) {
                Text("在电脑的设置 → 手机远程访问中生成二维码。无需配置 VPN，配对码仅在 5 分钟内有效。")
                if (scanned) {
                    Text("已扫描配对码", style = MaterialTheme.typography.titleSmall)
                    TextButton(onClick = { scanned = false; pairCode = ""; result = null }, enabled = !testing && paired == null) {
                        Text("改为粘贴配对码")
                    }
                } else {
                    OutlinedTextField(value = pairCode, onValueChange = { pairCode = it; paired = null }, label = { Text("配对码") }, visualTransformation = PasswordVisualTransformation(), maxLines = 3, enabled = !testing && paired == null, modifier = Modifier.fillMaxWidth())
                }
                Button(onClick = { pair(pairCode) }, enabled = !testing && pairCode.isNotBlank()) {
                    if (testing) CircularProgressIndicator(modifier = Modifier.size(18.dp), strokeWidth = 2.dp) else Text(if (paired != null) "重试保存" else if (scanned) "重试配对" else "配对并连接")
                }
                result?.let { Text(it, color = MaterialTheme.colorScheme.error) }
            } else {
                Text(
                    if (initial?.relay != null) "这台电脑通过加密中继连接。更换电脑身份或恢复凭据时，请在电脑上撤销此设备并重新扫码。" else "填写运行 easyagent 服务的主机地址和服务令牌（EA_SERVER_API_KEY，未设置时使用 EA_API_KEY）。手机需要先接入与主机相同的网络，例如同一 tailnet。",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it; result = null },
                    label = { Text("连接名称（可选）") },
                    placeholder = { Text("例如：迷你主机") },
                    singleLine = true,
                    enabled = !testing,
                    keyboardOptions = KeyboardOptions(imeAction = ImeAction.Next),
                    modifier = Modifier.fillMaxWidth(),
                )
                OutlinedTextField(
                    value = url,
                    onValueChange = { url = it; result = null },
                    label = { Text("服务地址") },
                    placeholder = { Text("http://主机地址:8080") },
                    singleLine = true,
                    enabled = !testing && initial?.relay == null,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, imeAction = ImeAction.Next),
                    modifier = Modifier.fillMaxWidth(),
                )
                OutlinedTextField(
                    value = token,
                    onValueChange = { token = it; result = null },
                    label = { Text("服务令牌") },
                    singleLine = true,
                    enabled = !testing && initial?.relay == null,
                    visualTransformation = if (showToken) VisualTransformation.None else PasswordVisualTransformation(),
                    trailingIcon = {
                        IconButton(onClick = { showToken = !showToken }) {
                            Icon(if (showToken) Icons.Filled.VisibilityOff else Icons.Filled.Visibility, contentDescription = if (showToken) "隐藏令牌" else "显示令牌")
                        }
                    },
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, imeAction = ImeAction.Done),
                    modifier = Modifier.fillMaxWidth(),
                )
                if (initial != null && initial.token == null) {
                    Text("无法读取已保存的令牌，请重新填写；服务未设置令牌时可留空。", color = MaterialTheme.colorScheme.error)
                }
                result?.let {
                    Text(
                        it,
                        style = MaterialTheme.typography.bodyMedium,
                        color = if (resultOk) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.error,
                    )
                }
                Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    OutlinedButton(
                        onClick = { verify() },
                        enabled = !testing && url.isNotBlank(),
                        modifier = Modifier.weight(1f),
                    ) {
                        if (testing) CircularProgressIndicator(modifier = Modifier.size(18.dp), strokeWidth = 2.dp) else Text("测试连接")
                    }
                    Button(
                        onClick = { save() },
                        enabled = !testing && url.isNotBlank(),
                        modifier = Modifier.weight(1f),
                    ) {
                        Text("保存并连接")
                    }
                }
                Text(
                    "保存后自动连接，离线时会自动重试。令牌按主机经 Android Keystore 加密保存在本机，不随备份上传。",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
    if (confirmLeave) {
        AlertDialog(
            onDismissRequest = { confirmLeave = false },
            title = { Text("连接尚未保存") },
            text = { Text("电脑已完成配对，但手机未保存凭据。返回后需要在电脑上撤销这次配对，再重新扫码。") },
            confirmButton = { TextButton(onClick = { confirmLeave = false }) { Text("继续保存") } },
            dismissButton = { TextButton(onClick = { confirmLeave = false; onCancel() }) { Text("仍要返回") } },
        )
    }
}
