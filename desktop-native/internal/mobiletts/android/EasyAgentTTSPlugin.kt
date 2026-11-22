package dev.mygo

import android.media.MediaPlayer
import android.os.Handler
import android.os.Looper
import android.speech.tts.TextToSpeech
import org.json.JSONObject
import java.io.File
import java.util.Locale

class EasyAgentTTSPlugin : MyGoPlugin {
    private var tts: TextToSpeech? = null
    private var player: MediaPlayer? = null
    private var temporary: File? = null
    private var generation = 0
    private val main = Handler(Looper.getMainLooper())

    init {
        PluginHost.context.cacheDir.listFiles()?.forEach {
            if (it.name.startsWith("easyagent-tts-") && it.name.endsWith(".mp3")) it.delete()
        }
    }

    private fun stopPlayback() {
        generation++
        runCatching { tts?.stop() }
        runCatching { player?.stop() }
        runCatching { player?.release() }
        player = null
        temporary?.delete()
        temporary = null
    }

    override fun call(method: String, args: JSONObject, reply: PluginReply) {
        when (method) {
			"cache" -> reply.success(JSONObject().put("path", PluginHost.context.cacheDir.absolutePath))
            "stop" -> { stopPlayback(); reply.success(null) }
            "status" -> reply.success(JSONObject().put("playing", (tts?.isSpeaking == true) || (runCatching { player?.isPlaying == true }.getOrDefault(false))))
            "file" -> {
                stopPlayback()
                val file = File(args.optString("path"))
                val root = PluginHost.context.cacheDir.canonicalFile
                if (!file.name.startsWith("easyagent-tts-") || file.canonicalFile.parentFile != root) {
                    reply.failure(PluginFailure("InvalidFile", "无效语音文件")); return
                }
                try {
                    val p = MediaPlayer()
                    player = p
                    temporary = file
                    p.setDataSource(file.absolutePath)
                    p.setOnCompletionListener { if (player === it) stopPlayback() }
                    p.setOnErrorListener { _, _, _ -> stopPlayback(); true }
                    p.prepare()
                    p.start()
                    reply.success(null)
                } catch (e: Exception) {
                    stopPlayback(); reply.failure(PluginFailure("PlaybackFailed", "无法播放语音"))
                }
            }
            "system" -> {
                stopPlayback()
                val id = generation
                val text = args.optString("text")
                if (text.isBlank()) { reply.failure(PluginFailure("Empty", "没有可朗读的文字")); return }
                fun speak(engine: TextToSpeech) {
                    if (id != generation) { reply.failure(PluginFailure("Cancelled", "朗读已取消")); return }
                    if (engine.setLanguage(Locale.SIMPLIFIED_CHINESE) < TextToSpeech.LANG_AVAILABLE) {
                        reply.failure(PluginFailure("Unavailable", "系统语音不支持中文")); return
                    }
                    val limit = TextToSpeech.getMaxSpeechInputLength().coerceAtMost(3500)
                    val pieces = text.chunked(limit)
                    for ((index, part) in pieces.withIndex()) {
                        if (engine.speak(part, if (index == 0) TextToSpeech.QUEUE_FLUSH else TextToSpeech.QUEUE_ADD, null, "easyagent-$id-$index") == TextToSpeech.ERROR) {
                            engine.stop(); reply.failure(PluginFailure("PlaybackFailed", "系统语音播放失败")); return
                        }
                    }
                    reply.success(null)
                }
                val existing = tts
                if (existing != null) { speak(existing); return }
                var created: TextToSpeech? = null
                created = TextToSpeech(PluginHost.context) { status ->
                    main.post {
                        val engine = created
                        if (status != TextToSpeech.SUCCESS || engine == null) {
                            engine?.shutdown(); reply.failure(PluginFailure("Unavailable", "系统语音引擎不可用"))
                        } else { tts = engine; speak(engine) }
                    }
                }
            }
            else -> reply.failure(PluginFailure("NoMethod", method))
        }
    }
}
