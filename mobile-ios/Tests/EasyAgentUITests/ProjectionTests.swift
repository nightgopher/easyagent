import Testing
import Foundation
@testable import EasyAgentUI
@testable import EasyAgentBackend

@Suite struct ChatProjectionTests {
    @Test func currentServerSnapshotEventAndReplayPreserveHistoryWithoutDuplicates() throws {
        var projection = ChatProjection()
        func envelope(_ json: String) throws -> WSServerMessage { try JSONDecoder().decode(WSServerMessage.self, from: Data(json.utf8)) }
        projection.apply(try envelope(#"{"type":"snapshot","session_id":"s1","seq":10,"run_id":"r1","messages":[{"role":"user","content":"历史问题"},{"role":"assistant","content":"历史答案"}],"run":{"id":"r1","state":"running","prompt":"新的问题"},"events":[]}"#))
        #expect(projection.entries.map(\.text) == ["历史问题", "历史答案", "新的问题"])
        let delta = try envelope(#"{"type":"event","session_id":"s1","seq":11,"run_id":"r1","event":{"type":"text_delta","text_delta":"新答"}}"#)
        projection.apply(delta)
        projection.apply(delta)
        projection.apply(try envelope(#"{"type":"replay","session_id":"s1","seq":12,"run":{"state":"completed"},"events":[{"type":"event","seq":11,"run_id":"r1","event":{"type":"text_delta","text_delta":"新答"}},{"type":"event","seq":12,"run_id":"r1","event":{"type":"text_delta","text_delta":"案"}}]}"#))
        #expect(projection.entries.map(\.text) == ["历史问题", "历史答案", "新的问题", "新答案"])
        #expect(projection.runState == "completed")
    }

    /// 构造服务端形状的 events 容器（[AgentEvent] 数组）。
    private func events(_ json: String) -> AnyDecodable? {
        guard let data = json.data(using: .utf8),
              let array = try? JSONSerialization.jsonObject(with: data) as? [[String: Any]] else { return nil }
        return AnyDecodable(array)
    }

    private func server(type: String, sessionID: String = "s1", runID: String = "r1",
                        state: String? = nil, events: AnyDecodable? = nil) -> WSServerMessage {
        WSServerMessage(type: type, sessionID: sessionID, runID: runID,
                        state: state, events: events)
    }

    @Test func textDeltasFoldIntoOneAssistantMessage() {
        var projection = ChatProjection()
        projection.apply(server(type: "accepted", state: "running"))
        projection.apply(server(type: "delta", events: events(#"[{"type":"text_delta","text_delta":"你"},{"type":"text_delta","text_delta":"好"}]"#)))
        projection.apply(server(type: "delta", events: events(#"[{"type":"text_delta","text_delta":"！"}]"#)))
        #expect(projection.entries.count == 1)
        #expect(projection.entries[0].text == "你好！")
        #expect(projection.entries[0].kind == .assistant)
        #expect(projection.runState == "running")
    }

    @Test func toolStartAndEndBuildToolCard() {
        var projection = ChatProjection()
        projection.apply(server(type: "delta", events: events(#"""
        [{"type":"tool_start","tool_call_id":"c1","tool_name":"read"},
         {"type":"text_delta","text_delta":"读到了"},
         {"type":"tool_end","tool_call_id":"c1"}]
        """#)))
        #expect(projection.entries.count == 2)
        #expect(projection.entries[0].kind == .tool)
        #expect(projection.entries[0].toolName == "read")
        #expect(projection.entries[0].toolStatus == "完成")
        #expect(projection.entries[1].text == "读到了")
    }

    @Test func toolEndWithErrorMarksFailed() {
        var projection = ChatProjection()
        projection.apply(server(type: "delta", events: events(#"""
        [{"type":"tool_start","tool_call_id":"c1","tool_name":"bash"},
         {"type":"tool_end","tool_call_id":"c1","is_error":true}]
        """#)))
        #expect(projection.entries.first?.toolStatus == "失败")
    }

    @Test func snapshotResetsThenReplays() {
        var projection = ChatProjection()
        projection.appendUser("旧消息")
        projection.apply(server(type: "snapshot", runID: "r2", events: events(#"[{"type":"text_delta","text_delta":"新投影"}]"#)))
        #expect(projection.entries.count == 1)
        #expect(projection.entries[0].text == "新投影")
    }

    @Test func thinkingDeltaSeparatesFromAssistantText() {
        var projection = ChatProjection()
        projection.apply(server(type: "delta", events: events(#"""
        [{"type":"thinking_delta","text_delta":"想想…"},
         {"type":"text_delta","text_delta":"答案是 42"}]
        """#)))
        #expect(projection.entries.count == 2)
        #expect(projection.entries[0].kind == .thought)
        #expect(projection.entries[1].text == "答案是 42")
    }

    @Test func unknownTypesIgnored() {
        var projection = ChatProjection()
        projection.apply(server(type: "pong"))
        projection.apply(server(type: "unsubscribed"))
        #expect(projection.entries.isEmpty)
    }

    @Test func errorEventBecomesAssistantEntry() {
        var projection = ChatProjection()
        projection.apply(server(type: "delta", events: events(#"[{"type":"error","error":"执行失败"}]"#)))
        #expect(projection.entries.last?.text == "执行失败")
    }
}

@Suite struct MemoryKeyStoreTests {
    @Test func saveAndReadBack() throws {
        let store = MemoryKeyStore()
        let id = UUID()
        try store.saveKey("secret", for: id)
        #expect(store.key(for: id) == "secret")
        store.removeKey(for: id)
        #expect(store.key(for: id) == nil)
    }

    @Test func hostsAreIndependent() throws {
        let store = MemoryKeyStore()
        let a = UUID(), b = UUID()
        try store.saveKey("a", for: a)
        try store.saveKey("b", for: b)
        #expect(store.key(for: a) == "a")
        #expect(store.key(for: b) == "b")
    }
}
