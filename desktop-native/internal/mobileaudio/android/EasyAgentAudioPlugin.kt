package dev.mygo

import android.Manifest
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaRecorder
import org.json.JSONObject
import java.io.File
import kotlin.concurrent.thread

class EasyAgentAudioPlugin : MyGoPlugin {
    private class Recording(val audio: AudioRecord, val file: File) {
        @Volatile var active = true
        @Volatile var error: String? = null
        @Volatile var level: Float = 0f
        lateinit var worker: Thread
    }

    private var recording: Recording? = null

    override fun call(method: String, args: JSONObject, reply: PluginReply) {
        when (method) {
            "start" -> {
                if (recording != null) {
                    reply.failure(PluginFailure("Busy", "已有录音进行中"))
                    return
                }
                PluginHost.requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO)) { granted ->
                    if (!granted) {
                        reply.failure(PluginFailure("PermissionDenied", "未获得麦克风权限"))
                        return@requestPermissions
                    }
                    try {
                        val minBuffer = AudioRecord.getMinBufferSize(16000, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
                        check(minBuffer > 0) { "录音设备不可用" }
                        val audio = AudioRecord(MediaRecorder.AudioSource.MIC, 16000, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT, maxOf(minBuffer, 4096))
                        if (audio.state != AudioRecord.STATE_INITIALIZED) {
                            audio.release()
                            throw IllegalStateException("录音设备初始化失败")
                        }
                        val file = try {
                            File.createTempFile("easyagent-voice-", ".pcm", PluginHost.context.cacheDir)
                        } catch (e: Exception) {
                            audio.release()
                            throw e
                        }
                        val r = Recording(audio, file)
                        try {
                            audio.startRecording()
                        } catch (e: Exception) {
                            audio.release()
                            file.delete()
                            throw e
                        }
                        recording = r
                        r.worker = thread(name = "easyagent-microphone") {
                            try {
                                file.outputStream().use { output ->
                                    val buffer = ByteArray(maxOf(minBuffer, 4096))
                                    var total = 0
                                    while (r.active && total < 19_200_000) {
                                        val n = audio.read(buffer, 0, minOf(buffer.size, 19_200_000 - total))
                                        if (n > 0) {
                                            output.write(buffer, 0, n)
                                            var peak = 0
                                            for (i in 0 until n - 1 step 2) {
                                                val sample = ((buffer[i].toInt() and 0xff) or (buffer[i + 1].toInt() shl 8)).toShort().toInt()
                                                peak = maxOf(peak, kotlin.math.abs(sample))
                                            }
                                            r.level = minOf(1f, peak / 8192f)
                                            total += n
                                        } else if (n < 0 && r.active) {
                                            throw IllegalStateException("录音设备读取失败")
                                        }
                                    }
                                    if (total >= 19_200_000) {
                                        r.error = "TooLong"
                                        r.active = false
                                        runCatching { audio.stop() }
                                    }
                                }
                            } catch (e: Exception) {
                                r.error = "录音保存失败"
                            }
                        }
                        reply.success(null)
                    } catch (e: Exception) {
                        reply.failure(PluginFailure("Unavailable", e.message ?: "录音设备不可用"))
                    }
                }
            }
            "level" -> reply.success(JSONObject().put("level", recording?.level ?: 0f))
            "stop" -> {
                val r = recording
                if (r == null) {
                    reply.failure(PluginFailure("NotRecording", "没有正在进行的录音"))
                    return
                }
                recording = null
                r.active = false
                thread(name = "easyagent-stop-microphone") {
                    runCatching { r.audio.stop() }
                    r.worker.join()
                    r.audio.release()
                    val error = r.error
                    if (error != null) {
                        r.file.delete()
                        reply.failure(PluginFailure(if (error == "TooLong") error else "RecordingFailed", error))
                    } else {
                        reply.success(JSONObject().put("path", r.file.absolutePath))
                    }
                }
            }
            else -> reply.failure(PluginFailure("NoMethod", method))
        }
    }
}
