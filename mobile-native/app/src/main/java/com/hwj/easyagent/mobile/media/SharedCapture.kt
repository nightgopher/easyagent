package com.hwj.easyagent.mobile.media

import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.core.content.IntentCompat
import com.hwj.easyagent.mobile.backend.wireJson
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import java.io.File
import java.util.UUID

@Serializable
data class SharedFile(val name: String, val storage: String)
@Serializable
data class SharedCapture(val id: String, val text: String, val files: List<SharedFile>, val errors: List<String> = emptyList(), val host: String = "", val session: String = "", val uploaded: Map<String, String> = emptyMap())

/** Copy granted URIs immediately; review survives process death without retaining grants. */
suspend fun receiveShare(context: Context, intent: Intent): SharedCapture = withContext(Dispatchers.IO) {
    val id = UUID.randomUUID().toString().replace("-", "")
    val directory = File(context.filesDir, "shares/$id").apply { mkdirs() }
    val uris = if (intent.action == Intent.ACTION_SEND_MULTIPLE)
        IntentCompat.getParcelableArrayListExtra(intent, Intent.EXTRA_STREAM, Uri::class.java).orEmpty()
    else listOfNotNull(IntentCompat.getParcelableExtra(intent, Intent.EXTRA_STREAM, Uri::class.java))
    val errors = mutableListOf<String>()
    if (uris.size > 8) errors.add("一次最多接收 8 个附件，其余附件未导入")
    val files = uris.distinct().take(8).mapNotNull { uri ->
        if (uri.scheme != "content") { errors.add("仅支持来源 App 授权的内容附件"); null }
        else {
            val (picked, error) = runCatching { readPicked(context.contentResolver, uri) }.getOrElse { null to "附件读取失败" }
            if (picked == null) { errors.add(error ?: "附件读取失败"); null }
            else {
                val storage = UUID.randomUUID().toString()
                File(directory, storage).writeBytes(picked.bytes)
                SharedFile(picked.name, storage)
            }
        }
    }
    val text = intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString().orEmpty().take(65536)
    SharedCapture(id, text, files, errors).also {
        File(directory, "capture.json").writeText(wireJson.encodeToString(SharedCapture.serializer(), it))
    }
}
fun loadShares(context: Context): List<SharedCapture> = File(context.filesDir, "shares").listFiles().orEmpty()
    .sortedBy { it.lastModified() }.mapNotNull { dir -> runCatching {
        wireJson.decodeFromString(SharedCapture.serializer(), File(dir, "capture.json").readText())
    }.getOrNull() }
fun removeShare(context: Context, capture: SharedCapture) { File(context.filesDir, "shares/${capture.id}").deleteRecursively() }

fun saveShare(context: Context, capture: SharedCapture) {
    val directory = File(context.filesDir, "shares/${capture.id}")
    val temp = File(directory, "capture.tmp")
    temp.writeText(wireJson.encodeToString(SharedCapture.serializer(), capture))
    check(temp.renameTo(File(directory, "capture.json"))) { "无法保存分享草稿" }
}
