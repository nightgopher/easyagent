package com.hwj.easyagent.mobile.ui

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.pdf.PdfRenderer
import android.os.ParcelFileDescriptor
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.hwj.easyagent.mobile.backend.*
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.*
import java.io.File
import java.net.URLEncoder
import java.util.Base64

private fun query(value: String) = URLEncoder.encode(value, "UTF-8")

@Composable
fun ResourceDialog(client: AgentClient, session: ChatSession, onDismiss: () -> Unit) {
    var tab by rememberSaveable { mutableStateOf("文件") }
    var path by rememberSaveable { mutableStateOf("") }
    var search by rememberSaveable { mutableStateOf("") }
    var entries by remember { mutableStateOf<List<JsonElement>>(emptyList()) }
    var error by remember { mutableStateOf("") }
    var loading by remember { mutableStateOf(false) }
    var patch by remember { mutableStateOf<String?>(null) }
    var selected by remember { mutableStateOf<String?>(null) }
    var saveAnswer by remember { mutableStateOf(false) }
    var savePath by rememberSaveable { mutableStateOf("notes/mobile-${System.currentTimeMillis()}.md") }
    var saving by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    LaunchedEffect(tab, path, search) {
        loading = true; error = ""; entries = emptyList()
        try {
            entries = when (tab) {
                "知识" -> { kotlinx.coroutines.delay(250); (client.api.get("/kb/entries?q=${query(search)}").obj()["entries"] as? JsonArray).orEmpty() }
                "变更" -> (client.api.get(ApiClient.sessionPath(session.id) + "/diff").obj()["files"] as? JsonArray).orEmpty()
                else -> (client.api.get("/workspace/list-dir?session_id=${query(session.id)}&path=${query(path)}") as? JsonArray).orEmpty()
            }
        } catch (e: ApiException) { error = e.message.orEmpty() }
        finally { loading = false }
    }
    patch?.let { diff -> AlertDialog(onDismissRequest = { patch = null }, title = { Text("文件差异") }, text = {
        LazyColumn { item { CodeBlock(diff.take(20000)) }; if (diff.length > 20000) item { Text("仅显示前 20000 字符") } }
    }, confirmButton = { TextButton(onClick = { patch = null }) { Text("关闭") } }) }
    selected?.let { file -> FilePreview(client, session, file, tab == "知识") { selected = null } }
    if (saveAnswer) AlertDialog(onDismissRequest = { if (!saving) saveAnswer = false }, title = { Text("保存最后一条回答") }, text = {
        Column {
            Text("在当前项目创建 Markdown，不覆盖同名文件。")
            OutlinedTextField(savePath, { savePath = it }, label = { Text("相对文件路径") })
            if (error.isNotBlank()) Text(error)
        }
    }, confirmButton = { Button(enabled = !saving && !session.running, onClick = {
        val answer = processTurns(session.messages, false, session.run?.state in setOf("cancelled", "interrupted", "failed")).lastOrNull()?.answer?.text
        if (answer == null) error = "当前没有已完成的回答可保存"
        else { saving = true; scope.launch {
            try {
                client.api.post(ApiClient.sessionPath(session.id) + "/artifacts", buildJsonObject { put("path", savePath); put("content", answer) })
                saveAnswer = false; selected = savePath; tab = "文件"
            } catch (e: ApiException) { error = e.message.orEmpty() }
            finally { saving = false }
        } }
    }) { Text("保存") } }, dismissButton = { TextButton(enabled = !saving, onClick = { saveAnswer = false }) { Text("取消") } })
    AlertDialog(onDismissRequest = onDismiss, title = { Text("文件与知识") }, text = {
        Column {
            Row { listOf("文件", "知识", "变更").forEach { label -> TextButton(onClick = { tab = label; path = "" }) { Text(if (tab == label) "● $label" else label) } } }
            if (tab == "知识") OutlinedTextField(search, { search = it }, label = { Text("搜索知识") })
            if (tab == "文件" && path.isNotEmpty()) TextButton(onClick = { path = "" }) { Text("回到项目根目录") }
            if (tab == "变更") Text("当前项目未提交变更（可能包含其他任务的修改）", style = MaterialTheme.typography.labelSmall)
            if (loading) CircularProgressIndicator()
            if (error.isNotEmpty()) Text(error, color = MaterialTheme.colorScheme.error)
            LazyColumn(Modifier.weight(1f, fill = false)) {
                items(entries) { entry ->
                    val item = entry.obj()
                    val file = item.getString("path")
                    TextButton(onClick = { if (tab == "变更") patch = item.getString("patch") else if (item["isDir"]?.str() == "true") path = file else selected = file }) {
                        Column {
                            Text(item.getString("title").ifBlank { item.getString("name").ifBlank { file } })
                            if (tab == "变更") Text("+${item["added"]?.str()} / -${item["removed"]?.str()}", style = MaterialTheme.typography.labelSmall)
                        }
                    }
                }
            }
            TextButton(enabled = !session.running, onClick = { error = ""; saveAnswer = true }) { Text("将回答保存为 Markdown") }
        }
    }, confirmButton = { TextButton(onClick = onDismiss) { Text("关闭") } })
}

@Composable
private fun FilePreview(client: AgentClient, session: ChatSession, path: String, knowledge: Boolean, onDismiss: () -> Unit) {
    val context = LocalContext.current
    var text by remember { mutableStateOf("") }
    var image by remember { mutableStateOf<Bitmap?>(null) }
    var error by remember { mutableStateOf("") }
    var loading by remember { mutableStateOf(true) }
    var page by rememberSaveable(path) { mutableStateOf(0) }
    var pages by remember { mutableStateOf(0) }
    var bytes by remember { mutableStateOf<ByteArray?>(null) }
    var mime by remember { mutableStateOf("") }
    LaunchedEffect(path) {
        try {
            if (knowledge) text = client.api.get("/kb/read?path=${query(path)}").obj().getString("content")
            else {
                val data = client.api.get("/workspace/file-data?session_id=${query(session.id)}&path=${query(path)}").obj()
                mime = data.getString("mimeType")
                bytes = withContext(Dispatchers.Default) { Base64.getDecoder().decode(data.getString("data")) }
                if (!mime.startsWith("image/") && mime != "application/pdf") {
                    if (mime.startsWith("text/") || path.endsWith(".md") || path.endsWith(".json")) text = bytes!!.decodeToString()
                    else error = "此格式暂不支持预览，可以引用给助手处理。"
                }
            }
        } catch (e: Exception) { if (e is kotlinx.coroutines.CancellationException) throw e; error = e.message.orEmpty() }
        finally { loading = false }
    }
    LaunchedEffect(bytes, page) {
        val data = bytes ?: return@LaunchedEffect
        try {
            image = withContext(Dispatchers.IO) {
                if (mime == "application/pdf") {
                    val temp = File.createTempFile("preview-", ".pdf", context.cacheDir)
                    try {
                        temp.writeBytes(data)
                        PdfRenderer(ParcelFileDescriptor.open(temp, ParcelFileDescriptor.MODE_READ_ONLY)).use { renderer ->
                            pages = renderer.pageCount
                            renderer.openPage(page).use { pdf ->
                                val width = minOf(1200, pdf.width)
                                Bitmap.createBitmap(width, (pdf.height.toLong() * width / pdf.width).toInt().coerceIn(1, 2400), Bitmap.Config.ARGB_8888).also {
                                    it.eraseColor(android.graphics.Color.WHITE)
                                    pdf.render(it, null, null, PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY)
                                }
                            }
                        }
                    } finally { temp.delete() }
                } else if (mime.startsWith("image/")) {
                    val options = BitmapFactory.Options().apply { inJustDecodeBounds = true }
                    BitmapFactory.decodeByteArray(data, 0, data.size, options)
                    options.inSampleSize = 1
                    while (maxOf(options.outWidth, options.outHeight) / options.inSampleSize > 1600) options.inSampleSize *= 2
                    options.inJustDecodeBounds = false
                    BitmapFactory.decodeByteArray(data, 0, data.size, options)
                } else null
            }
        } catch (e: Exception) { if (e is kotlinx.coroutines.CancellationException) throw e; error = "无法预览：${e.message}" }
    }
    AlertDialog(onDismissRequest = onDismiss, title = { Text(path.substringAfterLast('/')) }, text = {
        LazyColumn {
            if (loading) item { CircularProgressIndicator() }
            if (error.isNotBlank()) item { Text(error) }
            image?.let { bitmap -> item { Image(bitmap.asImageBitmap(), path, Modifier.fillMaxWidth()) } }
            if (text.isNotEmpty()) item { MarkdownView(text) }
            if (pages > 0) item { Row {
                TextButton(enabled = page > 0, onClick = { page-- }) { Text("上一页") }
                Text("${page + 1}/$pages")
                TextButton(enabled = page + 1 < pages, onClick = { page++ }) { Text("下一页") }
            } }
        }
    }, confirmButton = { TextButton(enabled = session.workspace.isNotEmpty() && session.references.size < 12, onClick = {
        val reference = buildJsonObject { put("scope", if (knowledge) "knowledge" else "workspace"); put("path", path); put("workspace", session.workspace) }
        if (reference !in session.references) session.references = session.references + reference
        client.touch(); onDismiss()
    }) { Text("引用到当前会话") } }, dismissButton = { TextButton(onClick = onDismiss) { Text("关闭") } })
}
