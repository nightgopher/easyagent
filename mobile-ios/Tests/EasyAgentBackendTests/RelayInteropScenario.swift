import Foundation
import Security
#if canImport(EasyAgentBackend)
@testable import EasyAgentBackend
#endif

// Also compiled as a small standalone runner on machines with only Command Line
// Tools, whose Swift distribution does not bundle the Testing framework.
enum RelayInteropScenario {
    struct Failure: Error { let message: String }
    static func check(_ condition: Bool, _ message: String) throws { if !condition { throw Failure(message: message) } }

    static func run() async throws {
        var root = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        while !FileManager.default.fileExists(atPath: root.appendingPathComponent("go.mod").path) && root.path != "/" { root.deleteLastPathComponent() }
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("easyagent-relay-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        defer { try? FileManager.default.removeItem(at: directory) }
        let output = directory.appendingPathComponent("fixture.json")
        let input = Pipe(), logs = Pipe()
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        process.arguments = ["go", "run", "./internal/relay/testdata/interop", output.path]
        process.currentDirectoryURL = URL(fileURLWithPath: root.path, isDirectory: true)
        var environment = ProcessInfo.processInfo.environment
        environment["PWD"] = root.path
        process.environment = environment
        process.standardInput = input; process.standardOutput = logs; process.standardError = logs
        try process.run()
        defer { try? input.fileHandleForWriting.close(); if process.isRunning { process.terminate() } }
        let limit = Date().addingTimeInterval(90)
        while !FileManager.default.fileExists(atPath: output.path) && process.isRunning && Date() < limit { try await Task.sleep(nanoseconds: 50_000_000) }
        if !FileManager.default.fileExists(atPath: output.path) {
            let detail = process.isRunning ? "startup timed out" : String(decoding: logs.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
            throw Failure(message: "Go relay fixture failed to start in \(root.path): \(detail)")
        }
        struct Fixture: Decodable { let code: RelayPairCode; let outer_certificate, control_url, control_token: String }
        let fixture = try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: output))
        let settings = URLSessionConfiguration.ephemeral
        settings.timeoutIntervalForRequest = 10
        settings.timeoutIntervalForResource = 20
        let outer = URLSession(configuration: settings, delegate: fixture.outer_certificate.isEmpty ? nil : FixtureTrust(certificate: Data(base64Encoded: fixture.outer_certificate)!), delegateQueue: nil)
        defer { outer.invalidateAndCancel() }
        let relay = fixture.code.relay
        let transport = try await RelayTransport.open(relay, outerSession: outer)
        defer { transport.close() }
        let session = transport.session!
        let base = try relay.baseURL()
        func request(_ path: String, token: String = "", body: Data? = nil) async throws -> (Data, Int) {
            var request = URLRequest(url: base.appendingPathComponent(path))
            request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
            if let body { request.httpMethod = "POST"; request.httpBody = body }
            let (data, response) = try await session.data(for: request)
            return (data, (response as! HTTPURLResponse).statusCode)
        }
        try check(try await request("sessions").1 == 401, "unpaired access")
        let (_, token) = try await fixture.code.claim(deviceName: "Apple fixture", session: session)
        let api = APIClient(host: Host(name: "fixture", baseURL: base, relay: relay), apiKeyProvider: { token }, session: session)
        try check(try await api.sessions().first?.id == "fixture", "actual session list contract")
        do { _ = try await fixture.code.claim(deviceName: "duplicate", session: session); throw Failure(message: "pair code reused") } catch is RelayError { }
        try check(try await request("sessions", token: token).1 == 200, "paired REST")
        try check(try await request("remote-access", token: token).1 == 403, "device can manage owner settings")
        let payload = Data(String(repeating: "attachment-content\n", count: 65536).utf8)
        try check(try await request("attachment", token: token).0 == payload, "large download")
        try check(try await request("echo", token: token, body: payload).0 == payload, "large upload")
        var components = URLComponents(url: base.appendingPathComponent("ws"), resolvingAgainstBaseURL: false)!
        components.scheme = "wss"
        var wsRequest = URLRequest(url: components.url!)
        wsRequest.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        let socket = session.webSocketTask(with: wsRequest)
        socket.resume()
        defer { socket.cancel(with: .goingAway, reason: nil) }
        try await socket.send(.string("private chat"))
        guard case .string("private chat") = try await socket.receive() else { throw Failure(message: "chat echo") }
        func control(_ path: String) async throws {
            var req = URLRequest(url: URL(string: fixture.control_url + path)!)
            req.setValue("Bearer " + fixture.control_token, forHTTPHeaderField: "Authorization")
            let (_, response) = try await URLSession.shared.data(for: req)
            try check((response as! HTTPURLResponse).statusCode == 200, "fixture control failed")
        }
        try await control("/restart")
        var recovered = false
        for _ in 0..<25 where !recovered {
            try await Task.sleep(nanoseconds: 200_000_000)
            recovered = (try? await request("sessions", token: token).1) == 200
        }
        try check(recovered, "host did not recover after broker restart")
        try await control("/revoke")
        try check(try await request("sessions", token: token).1 == 401, "revoked device accepted")
        let wrong = RelayConfig(relayURL: relay.relayURL, routeID: relay.routeID, fingerprint: "sha256:" + Data(repeating: 0, count: 32).base64EncodedString())
        let rejected = try await RelayTransport.open(wrong, outerSession: outer)
        defer { rejected.close() }
        do {
            _ = try await rejected.session.data(from: wrong.baseURL().appendingPathComponent("health"))
            throw Failure(message: "wrong fingerprint accepted")
        } catch is URLError { }
    }
}

private final class FixtureTrust: NSObject, URLSessionDelegate {
    let certificate: Data
    init(certificate: Data) { self.certificate = certificate }
    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard let trust = challenge.protectionSpace.serverTrust,
              let leaf = (SecTrustCopyCertificateChain(trust) as? [SecCertificate])?.first,
              SecCertificateCopyData(leaf) as Data == certificate else { completionHandler(.cancelAuthenticationChallenge, nil); return }
        SecTrustSetAnchorCertificates(trust, [leaf] as CFArray)
        SecTrustSetAnchorCertificatesOnly(trust, true)
        guard SecTrustEvaluateWithError(trust, nil) else { completionHandler(.cancelAuthenticationChallenge, nil); return }
        completionHandler(.useCredential, URLCredential(trust: trust))
    }
}
