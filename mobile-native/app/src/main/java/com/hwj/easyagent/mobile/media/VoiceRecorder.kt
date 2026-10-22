package com.hwj.easyagent.mobile.media

import android.annotation.SuppressLint
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaRecorder
import java.io.ByteArrayOutputStream
import kotlin.concurrent.thread

/**
 * Records 16 kHz mono PCM from the microphone into memory. The caller must
 * hold RECORD_AUDIO. Recording stops by itself after [MAX_SECONDS].
 */
class VoiceRecorder {
    private var record: AudioRecord? = null
    private var worker: Thread? = null
    private val pcm = ByteArrayOutputStream()
    @Volatile private var running = false

    val recording: Boolean get() = running

    @SuppressLint("MissingPermission") // checked by the caller before start()
    fun start(): Boolean {
        val minBuffer = AudioRecord.getMinBufferSize(SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
        if (minBuffer <= 0) return false
        val r = runCatching {
            AudioRecord(MediaRecorder.AudioSource.VOICE_RECOGNITION, SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT, minBuffer * 4)
        }.getOrNull() ?: return false
        if (r.state != AudioRecord.STATE_INITIALIZED) {
            r.release()
            return false
        }
        pcm.reset()
        record = r
        running = true
        r.startRecording()
        worker = thread(name = "voice-recorder") {
            val buffer = ByteArray(minBuffer)
            val limit = SAMPLE_RATE * 2 * MAX_SECONDS
            while (running && pcm.size() < limit) {
                val n = r.read(buffer, 0, buffer.size)
                if (n > 0) synchronized(pcm) { pcm.write(buffer, 0, n) }
            }
        }
        return true
    }

    /** Stops and returns the recording as WAV, or null if nothing was captured. */
    fun stop(): ByteArray? {
        if (!finish()) return null
        val data = synchronized(pcm) { pcm.toByteArray() }
        return if (data.size < SAMPLE_RATE / 5) null else wav(data, SAMPLE_RATE) // under 0.1 s is a mis-tap
    }

    fun cancel() {
        finish()
        pcm.reset()
    }

    private fun finish(): Boolean {
        val r = record ?: return false
        running = false
        worker?.join(1_000)
        runCatching { r.stop() }
        r.release()
        record = null
        worker = null
        return true
    }

    companion object {
        const val SAMPLE_RATE = 16_000
        const val MAX_SECONDS = 120
    }
}
