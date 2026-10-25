import Testing
import Foundation
@testable import EasyAgentBackend

// Swift Testing（随工具链分发，不依赖完整 Xcode；与 XCTest 等价的 SPM 测试运行器）。

@Suite struct WireTests {
    @Test func decodesSnakeCaseWireFields() throws {
        let json = """
        {"id":"s1","title":"修个 bug","last_active":1790000000,"created_at":1789990000,
         "workspace":"/tmp/w","application":"coding","archived":false,"pinned":true,
         "run_state":"running","pending_confirmations":2}
        """.data(using: .utf8)!
        let session = try JSONDecoder().decode(SessionSummary.self, from: json)
        #expect(session.id == "s1")
        #expect(session.lastActive == 1790000000)
        #expect(session.runState == "running")
        #expect(session.pendingConfirmations == 2)
        #expect(session.pinned)
    }

    @Test func encodesBackToWireNames() throws {
        let session = SessionSummary(id: "s2", title: "t", lastActive: 1, createdAt: 2,
                                      workspace: "/w", application: "coding",
                                      runState: "waiting_confirmation", pendingConfirmations: 1)
        let raw = String(data: try JSONEncoder().encode(session), encoding: .utf8) ?? ""
        #expect(raw.contains("\"last_active\":1"))
        #expect(raw.contains("\"run_state\":\"waiting_confirmation\""))
        #expect(raw.contains("\"pending_confirmations\":1"))
    }
}

@Suite(.serialized) struct HostStoreTests {
    private func freshDefaults() -> UserDefaults {
        let name = "test-\(UUID().uuidString)"
        return UserDefaults(suiteName: name)!
    }

    @Test func addPersistsAcrossStoreInstances() async {
        let defaults = freshDefaults()
        let store = HostStore(defaults: defaults)
        await store.add(Host(name: "迷你主机", baseURL: URL(string: "http://100.64.0.1:8080")!))
        let reloaded = HostStore(defaults: defaults)
        let hosts = await reloaded.list()
        #expect(hosts.count == 1)
        #expect(hosts.first?.name == "迷你主机")
    }

    @Test func duplicateBaseURLReplacesOldEntry() async {
        let store = HostStore(defaults: freshDefaults())
        await store.add(Host(name: "old", baseURL: URL(string: "http://10.0.0.2:8080")!))
        await store.add(Host(name: "new", baseURL: URL(string: "http://10.0.0.2:8080")!))
        let hosts = await store.list()
        #expect(hosts.count == 1)
        #expect(hosts.first?.name == "new")
    }

    @Test func removeDeletesOnlyTarget() async {
        let store = HostStore(defaults: freshDefaults())
        let a = Host(name: "a", baseURL: URL(string: "http://a:8080")!)
        let b = Host(name: "b", baseURL: URL(string: "http://b:8080")!)
        await store.add(a)
        await store.add(b)
        await store.remove(id: a.id)
        let hosts = await store.list()
        #expect(hosts.map(\.name) == ["b"])
    }
}

/// URLProtocol stub：静态 responder，测试内先设置再建 session。
final class StubURLProtocol: URLProtocol {
    nonisolated(unsafe) static var responder: (@Sendable (URLRequest) -> (Int, Data))?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let responder = Self.responder else {
            client?.urlProtocol(self, didFailWithError: URLError(.unsupportedURL))
            return
        }
        let (status, body) = responder(request)
        let response = HTTPURLResponse(url: request.url!, statusCode: status,
                                       httpVersion: "HTTP/1.1",
                                       headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    static func session() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        return URLSession(configuration: config)
    }
}

@Suite(.serialized) struct APIClientTests {
    @Test func unauthorizedMapsToAPIError() async {
        StubURLProtocol.responder = { _ in (401, Data("{\"error\":\"unauthorized\"}".utf8)) }
        defer { StubURLProtocol.responder = nil }
        let client = APIClient(host: Host(name: "h", baseURL: URL(string: "http://stub.local")!),
                               apiKeyProvider: { "k" }, session: StubURLProtocol.session())
        await #expect(throws: APIError.unauthorized) { _ = try await client.health() }
    }

    @Test func sessionsDecodesWrapper() async throws {
        StubURLProtocol.responder = { _ in
            (200, """
            {"sessions":[{"id":"s1","title":"hello","last_active":5,"created_at":4,
                          "workspace":"/w","application":"coding","archived":false,"pinned":false,
                          "run_state":"","pending_confirmations":0}]}
            """.data(using: .utf8)!)
        }
        defer { StubURLProtocol.responder = nil }
        let client = APIClient(host: Host(name: "h", baseURL: URL(string: "http://stub.local")!),
                               apiKeyProvider: { "k" }, session: StubURLProtocol.session())
        let sessions = try await client.sessions()
        #expect(sessions.count == 1)
        #expect(sessions.first?.id == "s1")
    }

    @Test func requestCarriesBearerToken() async throws {
        StubURLProtocol.responder = { request in
            #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer secret-key")
            return (200, Data("{\"status\":\"ok\",\"version\":\"v0\"}".utf8))
        }
        defer { StubURLProtocol.responder = nil }
        let client = APIClient(host: Host(name: "h", baseURL: URL(string: "http://stub.local")!),
                               apiKeyProvider: { "secret-key" }, session: StubURLProtocol.session())
        let health = try await client.health()
        #expect(health.status == "ok")
    }
}
