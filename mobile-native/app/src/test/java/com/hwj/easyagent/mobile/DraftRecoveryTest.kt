package com.hwj.easyagent.mobile

import com.hwj.easyagent.mobile.backend.*
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.setMain
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class DraftRecoveryTest {
    @Test fun `restart preserves draft attachments references and retry identity with host isolation`() {
        Dispatchers.setMain(UnconfinedTestDispatcher())
        val prefs = MemoryPreferences()
        val host = HostConfig("http://localhost:1", "secret", "host-a")
        val first = AgentClient(host, prefs)
        val restored = AgentClient(host, prefs)
        val other = AgentClient(host.copy(token = "other"), prefs)
        try {
            val session = first.chat("session")
            session.draft = "recover me"
            session.pendingRequest = "mobile-retry"
            session.sentDraft = "recover me"
            session.attachments = listOf(PendingAttachment(UploadedAttachment(id = "attachment", name = "photo.png"), null))
            session.sentAttachments = setOf("attachment")
            session.references = listOf(buildJsonObject { put("scope", "knowledge"); put("path", "notes/test.md") })
            session.sentReferences = session.references
            first.touch()
            val recovered = restored.chat("session")
            assertEquals("recover me", recovered.draft)
            assertEquals("mobile-retry", recovered.pendingRequest)
            assertEquals("attachment", recovered.attachments.single().attachment.id)
            assertEquals(session.sentAttachments, recovered.sentAttachments)
            assertEquals(session.references, recovered.references)
            assertEquals(session.sentReferences, recovered.sentReferences)
            assertEquals("", other.chat("session").draft)
        } finally { first.shutdown(); restored.shutdown(); other.shutdown(); Dispatchers.resetMain() }
    }
}
