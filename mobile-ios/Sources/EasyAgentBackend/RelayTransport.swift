import Foundation
import Network
import Security
import CryptoKit

public enum RelayError: LocalizedError {
    case invalidCode, expired, unavailable, wrongIdentity
    public var errorDescription: String? {
        switch self {
        case .invalidCode: return "配对码无效，请扫描电脑 EasyAgent 的二维码"
        case .expired: return "配对码已过期，请在电脑上重新生成"
        case .unavailable: return "无法连接电脑，请检查电脑是否在线后重试"
        case .wrongIdentity: return "电脑身份校验失败，请重新扫码配对"
        }
    }
}

public struct RelayConfig: Codable, Equatable, Sendable {
    public let relayURL: URL
    public let routeID: String
    public let fingerprint: String

    public init(relayURL: URL, routeID: String, fingerprint: String) {
        self.relayURL = relayURL; self.routeID = routeID; self.fingerprint = fingerprint
    }

    public func pin() throws -> Data {
        guard fingerprint.hasPrefix("sha256:"), let data = Data(base64Encoded: String(fingerprint.dropFirst(7))), data.count == 32 else { throw RelayError.invalidCode }
        return data
    }

    public func serverName() throws -> String { "ea-" + (try pin()).prefix(16).map { String(format: "%02x", $0) }.joined() + ".invalid" }
    public func baseURL() throws -> URL { URL(string: "https://" + (try serverName()))! }

    public func validate() throws {
        guard let u = URLComponents(url: relayURL, resolvingAgainstBaseURL: false), u.scheme == "https", u.host?.isEmpty == false,
              u.user == nil, u.password == nil, u.query == nil, u.fragment == nil, u.path.isEmpty || u.path == "/",
              Self.isToken(routeID) else { throw RelayError.invalidCode }
        _ = try pin()
    }

    static func isToken(_ value: String) -> Bool {
        value.utf8.count == 43 && value.unicodeScalars.allSatisfy { CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_").contains($0) }
    }

    var socketURL: URL {
        var u = URLComponents(url: relayURL, resolvingAgainstBaseURL: false)!
        u.scheme = "wss"; u.path = "/client"; u.queryItems = [URLQueryItem(name: "route_id", value: routeID)]
        return u.url!
    }
}

public struct RelayPairCode: Decodable, Sendable {
    public let v: Int
    public let relayURL: URL
    public let routeID, fingerprint, pairToken, hostName, expiresAt: String
    enum CodingKeys: String, CodingKey {
        case v
        case relayURL = "relay_url", routeID = "route_id", fingerprint = "host_pubkey_fp"
        case pairToken = "pair_token", hostName = "host_name", expiresAt = "expires_at"
    }
    public var relay: RelayConfig { RelayConfig(relayURL: relayURL, routeID: routeID, fingerprint: fingerprint) }

    public static func parse(_ text: String, now: Date = Date()) throws -> RelayPairCode {
        guard text.utf8.count <= 8192, let code = try? JSONDecoder().decode(Self.self, from: Data(text.utf8)),
              code.v == 1, RelayConfig.isToken(code.pairToken) else { throw RelayError.invalidCode }
        try code.relay.validate()
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        var expiry = formatter.date(from: code.expiresAt)
        if expiry == nil { formatter.formatOptions = [.withInternetDateTime]; expiry = formatter.date(from: code.expiresAt) }
        guard let expiry, expiry > now, expiry.timeIntervalSince(now) < 600 else { throw RelayError.expired }
        return code
    }

    public func claim(deviceName: String) async throws -> (Host, String) {
        let transport = try await RelayTransport.open(relay)
        defer { transport.close() }
        return try await claim(deviceName: deviceName, session: transport.session)
    }

    func claim(deviceName: String, session: URLSession) async throws -> (Host, String) {
        var request = URLRequest(url: try relay.baseURL().appendingPathComponent("pair/claim"))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["pair_token": pairToken, "name": String(deviceName.prefix(32))])
        let (data, response) = try await session.data(for: request)
        guard let response = response as? HTTPURLResponse else { throw RelayError.unavailable }
        guard response.statusCode == 201 else {
            if response.statusCode == 401 { throw RelayError.expired }
            throw RelayError.unavailable
        }
        struct Result: Decodable { let device_token: String }
        let token = try JSONDecoder().decode(Result.self, from: data).device_token
        guard RelayConfig.isToken(token) else { throw RelayError.invalidCode }
        return (Host(name: String(hostName.prefix(128)), baseURL: try relay.baseURL(), relay: relay), token)
    }
}

// Applies only to this host's inner session. The outer WSS keeps normal system trust.
final class PinnedHostDelegate: NSObject, URLSessionDelegate, URLSessionTaskDelegate {
    let config: RelayConfig
    init(_ config: RelayConfig) { self.config = config }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust else {
            completionHandler(.performDefaultHandling, nil); return
        }
        guard let name = try? config.serverName(), challenge.protectionSpace.host == name,
              let trust = challenge.protectionSpace.serverTrust,
              let certificate = (SecTrustCopyCertificateChain(trust) as? [SecCertificate])?.first,
              let key = SecCertificateCopyKey(certificate),
              let external = SecKeyCopyExternalRepresentation(key, nil) as Data?,
              let p256 = try? P256.Signing.PublicKey(x963Representation: external),
              let expected = try? config.pin(), Data(SHA256.hash(data: p256.derRepresentation)) == expected else {
            completionHandler(.cancelAuthenticationChallenge, nil); return
        }
        // Trust only this pinned leaf; still enforce hostname, validity and TLS usage.
        SecTrustSetPolicies(trust, SecPolicyCreateSSL(true, name as CFString))
        SecTrustSetAnchorCertificates(trust, [certificate] as CFArray)
        SecTrustSetAnchorCertificatesOnly(trust, true)
        SecTrustSetNetworkFetchAllowed(trust, false)
        guard SecTrustEvaluateWithError(trust, nil) else { completionHandler(.cancelAuthenticationChallenge, nil); return }
        completionHandler(.useCredential, URLCredential(trust: trust))
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
}

/// A per-host, loopback-only CONNECT proxy preserves URLSession REST and WebSocket
/// behavior. Bytes between it and the broker are already protected by inner TLS.
public final class RelayTransport: @unchecked Sendable {
    public private(set) var session: URLSession!
    private let listener: NWListener
    private let queue = DispatchQueue(label: "easyagent.relay")
    private let config: RelayConfig
    private let outerSession: URLSession
    private var pipes: [UUID: RelayPipe] = [:] // confined to queue
    private var closed = false
    private var waitingForListener = true // confined to queue

    private init(_ config: RelayConfig, outerSession: URLSession) throws {
        try config.validate()
        self.config = config; self.outerSession = outerSession
        let parameters = NWParameters.tcp
        parameters.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: parameters)
    }

    public static func open(_ config: RelayConfig) async throws -> RelayTransport {
        try await open(config, outerSession: .shared)
    }

    static func open(_ config: RelayConfig, outerSession: URLSession) async throws -> RelayTransport {
        let transport = try RelayTransport(config, outerSession: outerSession)
        try await transport.start()
        return transport
    }

    private func start() async throws {
        let port: NWEndpoint.Port = try await withCheckedThrowingContinuation { continuation in
            listener.stateUpdateHandler = { [weak self] state in
                guard let self, self.waitingForListener else { return }
                switch state {
                case .ready:
                    self.waitingForListener = false
                    if let port = self.listener.port { continuation.resume(returning: port) }
                    else { continuation.resume(throwing: RelayError.unavailable) }
                case .failed(let error): self.waitingForListener = false; continuation.resume(throwing: error)
                case .cancelled: self.waitingForListener = false; continuation.resume(throwing: RelayError.unavailable)
                default: break
                }
            }
            listener.newConnectionHandler = { [weak self] connection in
                guard let self, !self.closed, self.pipes.count < 16 else { connection.cancel(); return }
                let id = UUID()
                let pipe = RelayPipe(connection, config: self.config, outer: self.outerSession) { [weak self] in
                    self?.queue.async { [weak self] in self?.pipes[id] = nil }
                }
                self.pipes[id] = pipe
                Task { await pipe.start(queue: self.queue) }
            }
            listener.start(queue: queue)
        }
        let settings = URLSessionConfiguration.ephemeral
        settings.proxyConfigurations = [ProxyConfiguration(httpCONNECTProxy: .hostPort(host: "127.0.0.1", port: port))]
        settings.timeoutIntervalForRequest = 45
        settings.timeoutIntervalForResource = 120
        settings.httpShouldSetCookies = false
        settings.urlCache = nil
        session = URLSession(configuration: settings, delegate: PinnedHostDelegate(config), delegateQueue: nil)
    }

    public func close() {
        session?.invalidateAndCancel()
        queue.async { [self] in
            guard !closed else { return }
            closed = true
            listener.cancel()
            for pipe in pipes.values { Task { await pipe.close() } }
            pipes.removeAll()
        }
    }
    deinit { listener.cancel() }
}

private actor RelayPipe {
    let connection: NWConnection
    let config: RelayConfig
    let socket: URLSessionWebSocketTask
    let finished: @Sendable () -> Void
    var task: Task<Void, Never>?
    var closed = false

    init(_ connection: NWConnection, config: RelayConfig, outer: URLSession, finished: @escaping @Sendable () -> Void) {
        self.connection = connection; self.config = config; self.finished = finished
        var request = URLRequest(url: config.socketURL)
        request.timeoutInterval = 15
        socket = outer.webSocketTask(with: request)
        socket.maximumMessageSize = 65536
    }

    func start(queue: DispatchQueue) {
        guard !closed else { return }
        connection.start(queue: queue)
        task = Task { await run() }
    }

    func close() {
        guard !closed else { return }
        closed = true
        task?.cancel(); task = nil
        connection.cancel()
        socket.cancel(with: .goingAway, reason: nil)
        finished()
    }

    func run() async {
        let timeout = Task { try? await Task.sleep(nanoseconds: 15_000_000_000); if !Task.isCancelled { close() } }
        do {
            var header = Data()
            while !header.suffix(4).elementsEqual([13, 10, 13, 10]) {
                guard header.count < 8192 else { throw RelayError.unavailable }
                header.append(try await read(maximum: 1))
            }
            let first = String(decoding: header, as: UTF8.self).components(separatedBy: "\r\n").first
            guard first == "CONNECT \(try config.serverName()):443 HTTP/1.1" else { throw RelayError.unavailable }
            socket.resume()
            try await ping()
            try await write(Data("HTTP/1.1 200 Connection Established\r\n\r\n".utf8))
            timeout.cancel()
            await withThrowingTaskGroup(of: Void.self) { group in
                group.addTask { try await self.upload() }
                group.addTask { try await self.download() }
                group.addTask { try await self.heartbeat() }
                _ = try? await group.next()
                connection.cancel()
                socket.cancel(with: .goingAway, reason: nil)
                group.cancelAll()
            }
        } catch { /* Inner URLSession reports the connection failure without credentials. */ }
        timeout.cancel()
        close()
    }

    func read(maximum: Int) async throws -> Data {
        try await withCheckedThrowingContinuation { continuation in
            connection.receive(minimumIncompleteLength: 1, maximumLength: maximum) { data, _, _, error in
                if let error { continuation.resume(throwing: error) }
                else if let data, !data.isEmpty { continuation.resume(returning: data) }
                else { continuation.resume(throwing: RelayError.unavailable) }
            }
        }
    }
    func write(_ data: Data) async throws {
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            connection.send(content: data, completion: .contentProcessed { error in
                if let error { continuation.resume(throwing: error) } else { continuation.resume() }
            })
        }
    }
    func ping() async throws {
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            socket.sendPing { error in
                if let error { continuation.resume(throwing: error) } else { continuation.resume() }
            }
        }
    }
    func upload() async throws {
        while !Task.isCancelled {
            let data = try await read(maximum: 32 << 10)
            try await socket.send(.data(data))
        }
    }
    func download() async throws {
        while !Task.isCancelled {
            guard case .data(let data) = try await socket.receive(), data.count <= 65536 else { throw RelayError.unavailable }
            try await write(data)
        }
    }
    func heartbeat() async throws {
        while !Task.isCancelled { try await Task.sleep(nanoseconds: 20_000_000_000); try await ping() }
    }
}
