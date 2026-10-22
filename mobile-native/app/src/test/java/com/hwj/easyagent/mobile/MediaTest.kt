package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.AgentClient
import com.hwj.easyagent.mobile.backend.ChatSession
import com.hwj.easyagent.mobile.backend.Envelope
import com.hwj.easyagent.mobile.backend.HostConfig
import com.hwj.easyagent.mobile.backend.LinkStatus
import com.hwj.easyagent.mobile.backend.PendingAttachment
import com.hwj.easyagent.mobile.backend.UploadedAttachment
import com.hwj.easyagent.mobile.backend.wireJson
import com.hwj.easyagent.mobile.media.wav
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.setMain
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.Base64
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

@OptIn(ExperimentalCoroutinesApi::class)
class MediaTest {
    private fun withClient(block: suspend (MockWebServer, AgentClient) -> Unit) = runBlocking {
        val main = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
        val server = MockWebServer()
        server.start()
        Dispatchers.setMain(main)
        val client = AgentClient(HostConfig(server.url("/").toString().trimEnd('/'), "token"))
        try {
            withContext(main) { block(server, client) }
        } finally {
            withContext(main) { client.shutdown(); client.awaitShutdown() }
            Dispatchers.resetMain()
            main.close()
            server.shutdown()
        }
    }

    @Test
    fun `wav header describes 16 kHz mono PCM`() {
        val pcm = ByteArray(320) { it.toByte() }
        val data = wav(pcm, 16_000)
        val header = ByteBuffer.wrap(data).order(ByteOrder.LITTLE_ENDIAN)
        assertEquals("RIFF", String(data, 0, 4))
        assertEquals(36 + pcm.size, header.getInt(4))
        assertEquals("WAVE", String(data, 8, 4))
        assertEquals(1, header.getShort(22).toInt()) // mono
        assertEquals(16_000, header.getInt(24))
        assertEquals(16, header.getShort(34).toInt()) // bits per sample
        assertEquals(pcm.size, header.getInt(40))
        assertArrayEquals(pcm, data.copyOfRange(44, data.size))
    }

    @Test
    fun `uploaded attachment ids go with the next prompt and clear afterwards`() = withClient { server, client ->
        server.enqueue(MockResponse().setResponseCode(201).setBody("""{"id":"att_1","name":"a.png","mime_type":"image/png","size":3}"""))
        server.enqueue(MockResponse().setBody("""{"items":[],"revision":1}"""))
        val s = client.chat("s1")
        client.uploadAttachment(s, "a.png", byteArrayOf(1, 2, 3), null)
        assertEquals(1, s.uploading)
        val upload = withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS) }!!
        assertEquals("/sessions/s1/attachments", upload.path)
        val body = wireJson.parseToJsonElement(upload.body.readUtf8()).jsonObject
        assertEquals("a.png", body["name"]!!.jsonPrimitive.content)
        assertArrayEquals(byteArrayOf(1, 2, 3), Base64.getDecoder().decode(body["data"]!!.jsonPrimitive.content))
        withTimeout(5_000) { while (s.uploading > 0) delay(10) }
        assertEquals("att_1", s.attachments.single().attachment.id)

        // Queue path (run in progress) lets the test read the prompt body over HTTP.
        client.link.value = LinkStatus.CONNECTED
        s.running = true
        s.draft = "看看这张图"
        client.sendPrompt(s, s.draft)
        val queued = withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS) }!!
        val inputs = wireJson.parseToJsonElement(queued.body.readUtf8()).jsonObject["inputs"]!!.jsonObject
        assertEquals(listOf("att_1"), inputs["attachments"]!!.jsonArray.map { it.jsonPrimitive.content })
        withTimeout(5_000) { while (s.sending) delay(10) }
        assertTrue("attachments clear once the prompt is accepted", s.attachments.isEmpty())
    }

    @Test
    fun `acknowledging a prompt preserves attachments added while sending`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("""{"items":[],"revision":1}""").setBodyDelay(500, TimeUnit.MILLISECONDS))
        val s = client.chat("s1")
        s.attachments = listOf(PendingAttachment(UploadedAttachment(id = "sent"), null))
        client.link.value = LinkStatus.CONNECTED
        s.running = true
        s.draft = "first prompt"
        client.sendPrompt(s, s.draft)
        withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS) }!!
        s.attachments = s.attachments + PendingAttachment(UploadedAttachment(id = "next"), null)
        s.draft = "next prompt"
        withTimeout(5_000) { while (s.sending) delay(10) }
        assertEquals(listOf("next"), s.attachments.map { it.attachment.id })
        assertEquals("next prompt", s.draft)
    }

    @Test
    fun `oversized attachment is refused before upload`() = withClient { server, client ->
        val s = client.chat("s1")
        client.uploadAttachment(s, "big.bin", ByteArray(AgentClient.MAX_ATTACHMENT_BYTES + 1), null)
        assertEquals(0, server.requestCount)
        assertTrue(client.error.value!!.contains("4 MiB"))
    }

    @Test
    fun `transcription posts the recording as multipart file`() = withClient { server, client ->
        server.enqueue(MockResponse().setBody("""{"text":" 帮我部署 "}"""))
        val text = client.transcribe(wav(ByteArray(3200), 16_000))
        assertEquals("帮我部署", text)
        val request = withContext(Dispatchers.IO) { server.takeRequest(5, TimeUnit.SECONDS) }!!
        assertEquals("/asr/transcribe", request.path)
        assertTrue(request.getHeader("Content-Type")!!.startsWith("multipart/form-data"))
        val body = request.body.readUtf8()
        assertTrue(body.contains("name=\"file\"; filename=\"voice.wav\""))
        assertEquals("Bearer token", request.getHeader("Authorization"))
    }

    @Test
    fun `history images are kept on user messages and downloaded once`() = withClient { server, client ->
        val s = ChatSession("s1")
        s.reduce(
            wireJson.decodeFromString(
                Envelope.serializer(),
                """{"type":"snapshot","session_id":"s1","seq":1,"messages":[{"role":"user","content":"看图","images":[{"attachmentId":"img1","sessionId":"s1"}]}]}""",
            ),
        )
        val image = s.messages.single().images.single()
        assertEquals("img1", image.attachmentId)
        server.enqueue(MockResponse().setBody("PNGDATA"))
        assertEquals("PNGDATA", String(client.image("s1", image)))
        assertEquals("PNGDATA", String(client.image("s1", image)))
        assertEquals(1, server.requestCount)
        assertEquals("/sessions/s1/attachments/img1/raw", server.takeRequest().path)
    }
}
