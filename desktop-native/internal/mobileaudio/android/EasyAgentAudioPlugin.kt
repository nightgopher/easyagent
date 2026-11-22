package dev.mygo

import android.Manifest
import android.media.MediaRecorder
import org.json.JSONObject
import java.io.File
import kotlin.concurrent.thread

class EasyAgentAudioPlugin : MyGoPlugin {
    init {
        // Clean up only files owned by this plugin after an interrupted run.
        PluginHost.context.cacheDir.listFiles()?.forEach { file ->
            if (file.name.startsWith("easyagent-voice-") &&
                (file.name.endsWith(".pcm") || file.name.endsWith(".m4a"))) file.delete()
        }
    }

    private class Recording(val audio: MediaRecorder, val file: File) {
        @Volatile var tooLong = false
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
                    var audio: MediaRecorder? = null
                    var file: File? = null
                    try {
                        file = File.createTempFile("easyagent-voice-", ".m4a", PluginHost.context.cacheDir)
                        @Suppress("DEPRECATION")
                        val recorder = MediaRecorder()
                        audio = recorder
                        val r = Recording(recorder, file)
                        recorder.setAudioSource(MediaRecorder.AudioSource.MIC)
                        recorder.setOutputFormat(MediaRecorder.OutputFormat.MPEG_4)
                        recorder.setAudioEncoder(MediaRecorder.AudioEncoder.AAC)
                        recorder.setAudioChannels(1)
                        recorder.setAudioSamplingRate(16000)
                        recorder.setAudioEncodingBitRate(32000)
                        recorder.setOutputFile(file.absolutePath)
                        recorder.setMaxDuration(600_000)
                        recorder.setOnInfoListener { _, what, _ ->
                            if (what == MediaRecorder.MEDIA_RECORDER_INFO_MAX_DURATION_REACHED) r.tooLong = true
                        }
                        recorder.prepare()
                        recorder.start()
                        recording = r
                        reply.success(null)
                    } catch (e: Exception) {
                        runCatching { audio?.release() }
                        file?.delete()
                        reply.failure(PluginFailure("Unavailable", e.message ?: "录音设备不可用"))
                    }
                }
            }
            "level" -> {
                val level = try { (recording?.audio?.maxAmplitude ?: 0) / 8192f } catch (_: Exception) { 0f }
                reply.success(JSONObject().put("level", minOf(1f, level)))
            }
            "stop" -> {
                val r = recording
                if (r == null) {
                    reply.failure(PluginFailure("NotRecording", "没有正在进行的录音"))
                    return
                }
                recording = null
                thread(name = "easyagent-stop-microphone") {
                    val stopped = runCatching { r.audio.stop() }.isSuccess
                    runCatching { r.audio.release() }
                    if (!stopped || r.tooLong || r.file.length() == 0L) {
                        r.file.delete()
                        reply.failure(PluginFailure(if (r.tooLong) "TooLong" else "RecordingFailed", "录音失败"))
                    } else {
                        reply.success(JSONObject().put("path", r.file.absolutePath))
                    }
                }
            }
            else -> reply.failure(PluginFailure("NoMethod", method))
        }
    }
}
