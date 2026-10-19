package com.hwj.easyagent.mobile.ui

import androidx.activity.compose.BackHandler
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
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
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
) {
    val id = rememberSaveable { initial?.id ?: UUID.randomUUID().toString() }
    var name by rememberSaveable { mutableStateOf(initial?.name ?: "") }
    var url by rememberSaveable { mutableStateOf(initial?.url ?: "") }
    var token by remember { mutableStateOf(initial?.token ?: "") }
    var showToken by rememberSaveable { mutableStateOf(false) }
    var testing by remember { mutableStateOf(false) }
    var result by remember { mutableStateOf<String?>(null) }
    var resultOk by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    if (canCancel) BackHandler(onBack = onCancel)

    fun config(): HostConfig? = try {
        HostConfig(ApiClient.normalize(url), token.trim(), id, name.trim())
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
            val api = ApiClient(config.url, requireNotNull(config.token))
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

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(if (initial == null) "连接运行主机" else "编辑运行主机") },
                navigationIcon = {
                    if (canCancel) {
                        IconButton(onClick = onCancel) { Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "返回") }
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
            Text(
                "填写运行 easyagent 服务的主机地址和服务令牌（EA_SERVER_API_KEY，未设置时使用 EA_API_KEY）。手机需要先接入与主机相同的网络，例如同一 tailnet。",
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
                enabled = !testing,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, imeAction = ImeAction.Next),
                modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = token,
                onValueChange = { token = it; result = null },
                label = { Text("服务令牌") },
                singleLine = true,
                enabled = !testing,
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
