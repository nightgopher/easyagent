package com.hwj.easyagent.mobile.media

import android.content.ContentResolver
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.net.Uri
import android.provider.OpenableColumns
import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.readNBytesCompat
import java.io.ByteArrayOutputStream

/** A file picked on the phone, ready to upload. */
class PickedFile(val name: String, val bytes: ByteArray, val preview: ByteArray?)

private const val MAX_IMAGE_SIDE = 2560
private const val PREVIEW_SIDE = 256
const val MAX_IMAGE_SOURCE_BYTES = 32 * 1024 * 1024

/**
 * Reads a picked file. Images larger than the upload limit or [MAX_IMAGE_SIDE]
 * are scaled down and re-encoded as JPEG so phone photos fit; other files
 * must already be within the limit. Returns null with a reason on failure.
 */
fun readPicked(resolver: ContentResolver, uri: Uri): Pair<PickedFile?, String?> {
    val name = displayName(resolver, uri)
    val mime = resolver.getType(uri).orEmpty()
    val limit = if (mime.startsWith("image/")) MAX_IMAGE_SOURCE_BYTES else AgentClient.MAX_ATTACHMENT_BYTES
    val raw = runCatching { resolver.openInputStream(uri)?.use { it.readNBytesCompat(limit + 1) } }.getOrNull()
        ?: return null to "无法读取「$name」"
    if (raw.size > limit) return null to "「$name」超过 ${limit / 1024 / 1024} MiB，无法读取"
    if (!mime.startsWith("image/")) {
        if (raw.size > AgentClient.MAX_ATTACHMENT_BYTES) return null to "「$name」超过 4 MiB，无法上传"
        return PickedFile(name, raw, null) to null
    }
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(raw, 0, raw.size, bounds)
    if (bounds.outWidth <= 0) return PickedFile(name, raw, null) to null
    val preview = scaled(raw, bounds, PREVIEW_SIDE)?.let { jpeg(it, 80) }
    if (raw.size <= AgentClient.MAX_ATTACHMENT_BYTES && maxOf(bounds.outWidth, bounds.outHeight) <= MAX_IMAGE_SIDE) {
        return PickedFile(name, raw, preview) to null
    }
    val bitmap = scaled(raw, bounds, MAX_IMAGE_SIDE) ?: return null to "无法处理图片「$name」"
    for (quality in listOf(85, 70, 55)) {
        val data = jpeg(bitmap, quality)
        if (data.size <= AgentClient.MAX_ATTACHMENT_BYTES) {
            return PickedFile(name.substringBeforeLast('.') + ".jpg", data, preview) to null
        }
    }
    return null to "图片「$name」压缩后仍超过 4 MiB"
}

/** A camera capture taken into [bytes] (from TakePicture into a cache file). */
fun readCaptured(bytes: ByteArray): Pair<PickedFile?, String?> {
    if (bytes.size > MAX_IMAGE_SOURCE_BYTES) return null to "照片超过 32 MiB，无法读取"
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
    if (bounds.outWidth <= 0) return null to "无法读取拍摄的照片"
    val bitmap = scaled(bytes, bounds, MAX_IMAGE_SIDE) ?: return null to "无法读取拍摄的照片"
    val data = jpeg(bitmap, 85)
    if (data.size > AgentClient.MAX_ATTACHMENT_BYTES) return null to "照片压缩后仍超过 4 MiB"
    return PickedFile("photo-${System.currentTimeMillis()}.jpg", data, scaled(bytes, bounds, PREVIEW_SIDE)?.let { jpeg(it, 80) }) to null
}

private fun scaled(raw: ByteArray, bounds: BitmapFactory.Options, side: Int): Bitmap? {
    var sample = 1
    while (maxOf(bounds.outWidth, bounds.outHeight) / (sample * 2) >= side) sample *= 2
    val decoded = BitmapFactory.decodeByteArray(raw, 0, raw.size, BitmapFactory.Options().apply { inSampleSize = sample }) ?: return null
    val longest = maxOf(decoded.width, decoded.height)
    if (longest <= side) return decoded
    val ratio = side.toFloat() / longest
    return Bitmap.createScaledBitmap(decoded, (decoded.width * ratio).toInt().coerceAtLeast(1), (decoded.height * ratio).toInt().coerceAtLeast(1), true)
}

private fun jpeg(bitmap: Bitmap, quality: Int): ByteArray =
    ByteArrayOutputStream().also { bitmap.compress(Bitmap.CompressFormat.JPEG, quality, it) }.toByteArray()

private fun displayName(resolver: ContentResolver, uri: Uri): String {
    val name = runCatching {
        resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
            if (c.moveToFirst()) c.getString(0) else null
        }
    }.getOrNull()
    return (name ?: uri.lastPathSegment ?: "attachment").replace(Regex("[/\\\\\\u0000]"), "_").take(200)
}
