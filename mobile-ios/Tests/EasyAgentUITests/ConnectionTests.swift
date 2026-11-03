import Foundation
import Testing
@testable import EasyAgentBackend
@testable import EasyAgentUI

private actor ResponseGate {
    private var continuation: CheckedContinuation<Void, Never>?
    private var released = false
    func wait() async {
        if !released { await withCheckedContinuation { continuation = $0 } }
    }
    func release() { released = true; continuation?.resume(); continuation = nil }
}

private final class DelayedSessionsProtocol: URLProtocol {
    nonisolated(unsafe) static var response: (@Sendable (URLRequest) async -> Data)?
    override class func canInit(with request: URLRequest) -> Bool { request.url?.lastPathComponent == "sessions" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        guard let response = Self.response else { return }
        Task {
            let data = await response(request)
            client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: nil)!, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        }
    }
    override func stopLoading() {}
}

@Suite(.serialized) struct ConnectionTests {
    @Test(.timeLimit(.minutes(1))) @MainActor
    func lateSessionResponseCannotReplaceTheNewHost() async {
        let gate = ResponseGate()
        let (started, observed) = AsyncStream<Void>.makeStream()
        DelayedSessionsProtocol.response = { request in
            if request.url!.path.contains("slow") {
                observed.yield(())
                await gate.wait()
                return Data(#"[{"id":"old","title":"old host"}]"#.utf8)
            }
            return Data(#"[{"id":"new","title":"new host"}]"#.utf8)
        }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [DelayedSessionsProtocol.self]
        let session = URLSession(configuration: configuration)
        defer { session.invalidateAndCancel(); DelayedSessionsProtocol.response = nil; observed.finish() }
        let suite = "connection-test-\(UUID())"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let model = AppModel(hostStore: HostStore(defaults: defaults), keyStore: MemoryKeyStore(), session: session)
        // Only the HTTP fixture is handled; WebSocket attempts fail locally.
        let slow = Host(name: "slow", baseURL: URL(string: "http://127.0.0.1:1/slow")!)
        let fast = Host(name: "fast", baseURL: URL(string: "http://127.0.0.1:1/fast")!)
        model.hosts = [slow, fast]
        let old = Task { await model.connect(hostID: slow.id) }
        for await _ in started { break }
        await model.connect(hostID: fast.id)
        #expect(model.sessions.map(\.id) == ["new"])
        await gate.release()
        await old.value
        #expect(model.currentHostID == fast.id)
        #expect(model.sessions.map(\.id) == ["new"])
        await model.disconnect()
        #expect(model.client == nil)
        #expect(!model.linkOnline)
    }
}
