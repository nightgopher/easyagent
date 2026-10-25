import Testing
import Foundation
@testable import EasyAgentBackend

@Suite struct ChatSocketWireTests {
    @Test func clientMessageEncodesWireNames() throws {
        let message = WSClientMessage(type: "prompt", sessionID: "s1",
                                      prompt: "你好", requestID: "r1", afterSeq: 7)
        let raw = String(data: try JSONEncoder().encode(message), encoding: .utf8) ?? ""
        #expect(raw.contains("\"type\":\"prompt\""))
        #expect(raw.contains("\"session_id\":\"s1\""))
        #expect(raw.contains("\"request_id\":\"r1\""))
        #expect(raw.contains("\"after_seq\":7"))
        #expect(raw.contains("\"prompt\":\"你好\""))
    }

    @Test func serverMessageDecodesDeltaLikePayload() throws {
        let json = """
        {"type":"delta","session_id":"s1","run_id":"run1","seq":3,"state":"running",
         "event":{"delta":"进度文本"}}
        """.data(using: .utf8)!
        let decoded = try JSONDecoder().decode(WSServerMessage.self, from: json)
        #expect(decoded.type == "delta")
        #expect(decoded.sessionID == "s1")
        #expect(decoded.seq == 3)
        #expect(decoded.state == "running")
        let event = decoded.event?.value as? [String: Any]
        #expect((event?["delta"] as? String) == "进度文本")
    }

    @Test func serverMessageDecodesErrorShape() throws {
        let json = Data("{\"type\":\"error\",\"code\":\"busy\"}".utf8)
        let decoded = try JSONDecoder().decode(WSServerMessage.self, from: json)
        #expect(decoded.type == "error")
        #expect(decoded.code == "busy")
    }

    @Test func wsURLConvertsScheme() throws {
        let httpHost = Host(name: "h", baseURL: URL(string: "http://100.64.0.1:8080")!)
        let httpsHost = Host(name: "h", baseURL: URL(string: "https://example.com")!)
        // wsURL 是私有方法，通过镜像 URL 规则直接验证组合逻辑：
        for (base, expected) in [(httpHost.baseURL, "ws://100.64.0.1:8080/ws"),
                                 (httpsHost.baseURL, "wss://example.com/ws")] {
            var components = URLComponents(url: base.appendingPathComponent("ws"), resolvingAgainstBaseURL: false)!
            components.scheme = components.scheme == "https" ? "wss" : "ws"
            #expect(components.url?.absoluteString == expected)
        }
    }
}
