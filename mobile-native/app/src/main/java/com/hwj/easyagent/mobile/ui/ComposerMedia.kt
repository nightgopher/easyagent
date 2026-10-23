package com.hwj.easyagent.mobile.ui

import android.Manifest
import android.content.pm.PackageManager
import android.graphics.BitmapFactory
import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.PickVisualMediaRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.Image
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.InsertDriveFile
import androidx.compose.material.icons.filled.Mic
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilledIconButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TextField
import androidx.compose.material3.TextFieldDefaults
import androidx.compose.ui.graphics.Color
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.ApiException
import com.hwj.easyagent.mobile.backend.ChatSession
import com.hwj.easyagent.mobile.backend.HistoryImage
import com.hwj.easyagent.mobile.backend.LinkStatus
import com.hwj.easyagent.mobile.backend.readNBytesCompat
import com.hwj.easyagent.mobile.media.MAX_IMAGE_SOURCE_BYTES
import com.hwj.easyagent.mobile.media.PickedFile
import com.hwj.easyagent.mobile.media.VoiceRecorder
import com.hwj.easyagent.mobile.media.readCaptured
import com.hwj.easyagent.mobile.media.readPicked
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File

/**
 * Message input with attachments (camera, photos, files) and voice input. A
 * recording is transcribed by the host and inserted into the draft for
 * editing, never sent directly (#254, #255).
 */
@Composable
fun MediaComposer(client: AgentClient, session: ChatSession, link: LinkStatus, queued: Boolean) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var menu by remember { mutableStateOf(false) }
    val recorder = remember { VoiceRecorder() }
    var recordingSince by remember { mutableLongStateOf(0L) }
    var transcribing by remember { mutableStateOf(false) }
    var failedRecording by remember { mutableStateOf<ByteArray?>(null) }
    DisposableEffect(Unit) { onDispose { recorder.cancel() } }

    fun upload(results: List<Pair<PickedFile?, String?>>) {
        for ((file, problem) in results) {
            if (file != null) client.uploadAttachment(session, file.name, file.bytes, file.preview)
            else if (problem != null) client.error.value = problem
        }
    }
    fun pickUris(uris: List<Uri>) {
        if (uris.isEmpty()) return
        scope.launch {
            upload(withContext(Dispatchers.IO) { uris.map { readPicked(context.contentResolver, it) } })
        }
    }
    val photos = rememberLauncherForActivityResult(ActivityResultContracts.PickMultipleVisualMedia(9)) { pickUris(it) }
    val files = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { pickUris(it) }
    var cameraFile by remember { mutableStateOf<File?>(null) }
    val camera = rememberLauncherForActivityResult(ActivityResultContracts.TakePicture()) { taken ->
        val file = cameraFile ?: return@rememberLauncherForActivityResult
        cameraFile = null
        if (!taken) {
            file.delete()
            return@rememberLauncherForActivityResult
        }
        scope.launch {
            val result = withContext(Dispatchers.IO) { readCaptured(file.inputStream().use { it.readNBytesCompat(MAX_IMAGE_SOURCE_BYTES + 1) }).also { file.delete() } }
            upload(listOf(result))
        }
    }

    fun transcribe(wav: ByteArray) {
        transcribing = true
        scope.launch {
            try {
                val text = client.transcribe(wav)
                if (text.isEmpty()) {
                    client.error.value = "没有识别到语音内容"
                } else {
                    session.draft = if (session.draft.isBlank()) text else session.draft.trimEnd() + " " + text
                    client.touch()
                }
                failedRecording = null
            } catch (e: ApiException) {
                failedRecording = wav
                client.error.value = "语音转写失败：" + e.message
            } finally {
                transcribing = false
            }
        }
    }
    LaunchedEffect(recordingSince) {
        if (recordingSince > 0) {
            delay((VoiceRecorder.MAX_SECONDS * 1000L - (System.currentTimeMillis() - recordingSince)).coerceAtLeast(0))
            recordingSince = 0
            recorder.stop()?.let(::transcribe)
        }
    }
    fun startRecording() {
        failedRecording = null
        if (recorder.start()) recordingSince = System.currentTimeMillis()
        else client.error.value = "无法打开麦克风"
    }
    val micPermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) startRecording() else client.error.value = "需要麦克风权限才能语音输入"
    }

    val connected = link == LinkStatus.CONNECTED
    val recording = recordingSince > 0
    Surface(tonalElevation = 2.dp) {
        Column(modifier = Modifier.navigationBarsPadding().imePadding().padding(horizontal = 12.dp, vertical = 8.dp)) {
            if (session.attachments.isNotEmpty() || session.uploading > 0) {
                Row(
                    modifier = Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(bottom = 8.dp),
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    session.attachments.forEach { pending ->
                        AttachmentChip(pending.attachment.name, pending.preview) { client.removeAttachment(session, pending.attachment.id) }
                    }
                    if (session.uploading > 0) {
                        Box(Modifier.size(56.dp), contentAlignment = Alignment.Center) {
                            CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                        }
                    }
                }
            }
            failedRecording?.let { wav ->
                Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(bottom = 6.dp)) {
                    Text("语音转写失败", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error, modifier = Modifier.weight(1f))
                    TextButton(onClick = { transcribe(wav) }, enabled = !transcribing && connected) { Text("重试") }
                    TextButton(onClick = { failedRecording = null }) { Text("放弃") }
                }
            }
            if (recording) {
                RecordingBar(
                    since = recordingSince,
                    modifier = Modifier.fillMaxWidth(),
                    onCancel = { recorder.cancel(); recordingSince = 0 },
                    onDone = {
                        recordingSince = 0
                        val wav = recorder.stop()
                        if (wav == null) client.error.value = "录音太短" else transcribe(wav)
                    },
                )
            } else {
                // One pill: attachments inside on the left, and a single trailing
                // action that is the mic while empty and send once there is text.
                Surface(
                    shape = RoundedCornerShape(24.dp),
                    color = MaterialTheme.colorScheme.surface,
                    border = BorderStroke(1.dp, MaterialTheme.colorScheme.outlineVariant),
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Row(verticalAlignment = Alignment.Bottom, modifier = Modifier.padding(4.dp)) {
                        Box {
                            IconButton(onClick = { menu = true }, enabled = connected) {
                                Icon(Icons.Filled.Add, contentDescription = "添加附件")
                            }
                            DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                                DropdownMenuItem(text = { Text("拍照") }, onClick = {
                                    menu = false
                                    val dir = File(context.cacheDir, "camera").apply { mkdirs() }
                                    val file = File(dir, "capture-${System.currentTimeMillis()}.jpg")
                                    cameraFile = file
                                    runCatching {
                                        camera.launch(FileProvider.getUriForFile(context, context.packageName + ".files", file))
                                    }.onFailure { client.error.value = "无法打开相机" }
                                })
                                DropdownMenuItem(text = { Text("从相册选择") }, onClick = {
                                    menu = false
                                    photos.launch(PickVisualMediaRequest(ActivityResultContracts.PickVisualMedia.ImageOnly))
                                })
                                DropdownMenuItem(text = { Text("选择文件") }, onClick = {
                                    menu = false
                                    files.launch(arrayOf("*/*"))
                                })
                            }
                        }
                        TextField(
                            value = session.draft,
                            onValueChange = { session.draft = it; client.touch() },
                            modifier = Modifier.weight(1f),
                            placeholder = { Text(if (transcribing) "正在转写语音…" else if (queued) "排队发送下一条…" else "发消息…", maxLines = 1) },
                            maxLines = 6,
                            colors = TextFieldDefaults.colors(
                                focusedContainerColor = Color.Transparent,
                                unfocusedContainerColor = Color.Transparent,
                                disabledContainerColor = Color.Transparent,
                                focusedIndicatorColor = Color.Transparent,
                                unfocusedIndicatorColor = Color.Transparent,
                                disabledIndicatorColor = Color.Transparent,
                            ),
                        )
                        val hasText = session.draft.isNotBlank()
                        when {
                            session.sending -> Box(Modifier.size(48.dp), contentAlignment = Alignment.Center) {
                                CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                            }
                            hasText -> {
                                val canSend = session.uploading == 0 && connected
                                FilledIconButton(onClick = { client.sendPrompt(session, session.draft) }, enabled = canSend, modifier = Modifier.size(48.dp)) {
                                    Icon(Icons.AutoMirrored.Filled.Send, contentDescription = "发送")
                                }
                            }
                            transcribing -> Box(Modifier.size(48.dp), contentAlignment = Alignment.Center) {
                                CircularProgressIndicator(Modifier.size(20.dp), strokeWidth = 2.dp)
                            }
                            else -> IconButton(
                                onClick = {
                                    if (ContextCompat.checkSelfPermission(context, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) startRecording()
                                    else micPermission.launch(Manifest.permission.RECORD_AUDIO)
                                },
                                enabled = connected,
                                modifier = Modifier.size(48.dp),
                            ) {
                                Icon(Icons.Filled.Mic, contentDescription = "语音输入")
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun RecordingBar(since: Long, modifier: Modifier, onCancel: () -> Unit, onDone: () -> Unit) {
    var now by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(since) {
        while (true) {
            now = System.currentTimeMillis()
            delay(250)
        }
    }
    val seconds = ((now - since) / 1000).coerceAtLeast(0)
    Surface(shape = RoundedCornerShape(20.dp), color = MaterialTheme.colorScheme.errorContainer, modifier = modifier.heightIn(min = 56.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(horizontal = 8.dp)) {
            IconButton(onClick = onCancel) { Icon(Icons.Filled.Close, contentDescription = "取消录音") }
            Text(
                "正在录音 %d:%02d".format(seconds / 60, seconds % 60),
                color = MaterialTheme.colorScheme.onErrorContainer,
                modifier = Modifier.weight(1f),
            )
            IconButton(onClick = onDone) { Icon(Icons.Filled.Check, contentDescription = "结束录音并转写") }
        }
    }
}

@Composable
private fun AttachmentChip(name: String, preview: ByteArray?, onRemove: () -> Unit) {
    val bitmap = remember(preview) { preview?.let { BitmapFactory.decodeByteArray(it, 0, it.size)?.asImageBitmap() } }
    Box {
        Surface(shape = RoundedCornerShape(10.dp), color = MaterialTheme.colorScheme.surfaceVariant) {
            if (bitmap != null) {
                Image(bitmap, contentDescription = name, contentScale = ContentScale.Crop, modifier = Modifier.size(56.dp))
            } else {
                Column(Modifier.size(width = 96.dp, height = 56.dp).padding(6.dp)) {
                    Icon(Icons.Filled.InsertDriveFile, contentDescription = null, modifier = Modifier.size(18.dp))
                    Text(name, style = MaterialTheme.typography.labelSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
            }
        }
        Surface(shape = RoundedCornerShape(50), color = MaterialTheme.colorScheme.inverseSurface, modifier = Modifier.align(Alignment.TopEnd).size(20.dp).clickable(onClick = onRemove)) {
            Icon(Icons.Filled.Close, contentDescription = "移除附件：$name", tint = MaterialTheme.colorScheme.inverseOnSurface, modifier = Modifier.padding(3.dp))
        }
    }
}

/** Thumbnails of the images in a user message; tapping one shows it full size. */
@Composable
fun MessageImages(client: AgentClient, sessionId: String, images: List<HistoryImage>) {
    var shown by remember { mutableStateOf<ImageBitmap?>(null) }
    Row(horizontalArrangement = Arrangement.spacedBy(6.dp), modifier = Modifier.horizontalScroll(rememberScrollState())) {
        images.forEach { image ->
            var bitmap by remember(image) { mutableStateOf<ImageBitmap?>(null) }
            var failed by remember(image) { mutableStateOf(false) }
            LaunchedEffect(image) {
                failed = false
                bitmap = runCatching {
                    val bytes = client.image(sessionId, image)
                    withContext(Dispatchers.Default) { BitmapFactory.decodeByteArray(bytes, 0, bytes.size)?.asImageBitmap() }
                }.getOrNull()
                failed = bitmap == null
            }
            Surface(shape = RoundedCornerShape(10.dp), color = MaterialTheme.colorScheme.surfaceVariant, modifier = Modifier.size(96.dp)) {
                val current = bitmap
                when {
                    current != null -> Image(
                        current,
                        contentDescription = "附件图片",
                        contentScale = ContentScale.Crop,
                        modifier = Modifier.clip(RoundedCornerShape(10.dp)).clickable { shown = current },
                    )
                    failed -> Box(contentAlignment = Alignment.Center) { Text("图片加载失败", style = MaterialTheme.typography.labelSmall) }
                    else -> Box(contentAlignment = Alignment.Center) { CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp) }
                }
            }
        }
    }
    shown?.let { bitmap ->
        AlertDialog(
            onDismissRequest = { shown = null },
            confirmButton = { TextButton(onClick = { shown = null }) { Text("关闭") } },
            text = { Image(bitmap, contentDescription = "原图", contentScale = ContentScale.Fit, modifier = Modifier.fillMaxWidth()) },
        )
    }
}
