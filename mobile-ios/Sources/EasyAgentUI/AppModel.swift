import Foundation
import Observation
import EasyAgentBackend

public typealias AgentHost = EasyAgentBackend.Host

/// App 的根状态：当前主机、连接与投影。@MainActor + @Observable 供 SwiftUI 直接用。
@MainActor @Observable
public final class AppModel {
    public var hosts: [AgentHost] = []
    public var currentHostID: UUID?
    public var sessions: [SessionSummary] = []
    public var linkOnline = false
    public var errorMessage: String?
    public var projection = ChatProjection()
    public var activeSessionID: String?

    public private(set) var client: APIClient?
    private var socket: ChatSocket?
    private let hostStore: HostStore
    private let keyStore: any APIKeyStore
    private let directSession: URLSession
    private var pumpTask: Task<Void, Never>?
    private var relayTransport: RelayTransport?
    private var connectionGeneration: UInt64 = 0

    public init(hostStore: HostStore, keyStore: any APIKeyStore = SystemKeychainStore(), session: URLSession = .shared) {
        self.hostStore = hostStore
        self.keyStore = keyStore
        self.directSession = session
    }

    public var currentHost: AgentHost? {
        hosts.first { $0.id == currentHostID }
    }

    public func bootstrap() async {
        hosts = await hostStore.list()
        if currentHostID == nil { currentHostID = hosts.last?.id }
        if let id = currentHostID { await connect(hostID: id) }
    }

    public func add(host: AgentHost) async {
        await hostStore.add(host)
        hosts = await hostStore.list()
    }

    public func connect(hostID: UUID) async {
        guard let host = hosts.first(where: { $0.id == hostID }) else { return }
        let generation = connectionGeneration &+ 1
        await disconnect()
        guard generation == connectionGeneration else { return }
        if currentHostID != hostID { activeSessionID = nil }
        currentHostID = hostID
        let session: URLSession
        do {
            if let relay = host.relay {
                guard host.baseURL == (try relay.baseURL()) else { throw RelayError.invalidCode }
                let transport = try await RelayTransport.open(relay)
                guard generation == connectionGeneration else { transport.close(); return }
                relayTransport = transport
                session = transport.session
            } else { session = directSession }
        } catch {
            if generation == connectionGeneration { errorMessage = error.localizedDescription }
            return
        }
        let keyStore = self.keyStore
        let provider: @Sendable () -> String = { keyStore.key(for: hostID) ?? "" }
        client = APIClient(host: host, apiKeyProvider: provider, session: session)
        let socket = ChatSocket(host: host, apiKeyProvider: provider, session: session)
        self.socket = socket
        projection = ChatProjection()
        startPump(on: socket)
        await socket.connect()
        guard generation == connectionGeneration else { await socket.disconnect(); return }
        if let id = activeSessionID { await socket.subscribe(sessionID: id) }
        if generation == connectionGeneration { await reloadSessions() }
    }

    public func disconnect() async {
        connectionGeneration &+= 1
        pumpTask?.cancel()
        pumpTask = nil
        let previous = socket
        socket = nil
        client = nil
        relayTransport?.close()
        relayTransport = nil
        linkOnline = false
        await previous?.disconnect()
    }

    public func savePairedHost(_ paired: AgentHost, token: String) async throws {
        var host = paired
        if let saved = hosts.first(where: { $0.baseURL == host.baseURL && $0.relay != nil }) { host.id = saved.id }
        try keyStore.saveKey(token, for: host.id)
        await add(host: host)
        await connect(hostID: host.id)
    }

    public func reloadSessions() async {
        guard let client else { return }
        let generation = connectionGeneration
        do {
            let loaded = try await client.sessions()
            guard generation == connectionGeneration else { return }
            sessions = loaded
            errorMessage = nil
        } catch let error as APIError {
            if generation == connectionGeneration { errorMessage = describe(error) }
        } catch {
            if generation == connectionGeneration { errorMessage = error.localizedDescription }
        }
    }

    public func open(sessionID: String) async {
        guard activeSessionID != sessionID else { return }
        if let previous = activeSessionID, previous != sessionID { await socket?.unsubscribe(sessionID: previous) }
        activeSessionID = sessionID
        projection = ChatProjection()
        guard let socket else { return }
        await socket.subscribe(sessionID: sessionID)
    }

    public func send(draft: String) async -> Bool {
        let trimmed = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, let sessionID = activeSessionID, let socket else { return false }
        let requestID = UUID().uuidString
        guard await socket.sendPrompt(sessionID: sessionID, requestID: requestID, prompt: trimmed) else {
            errorMessage = "连接已断开，草稿已保留"
            return false
        }
        return true
    }

    // MARK: - internals

    private func startPump(on socket: ChatSocket) {
        pumpTask?.cancel()
        let generation = connectionGeneration
        pumpTask = Task { [weak self] in
            let stream = await socket.events()
            for await event in stream {
                await MainActor.run {
                    guard !Task.isCancelled, self?.connectionGeneration == generation else { return }
                    self?.handle(event)
                }
            }
        }
    }

    private func handle(_ event: ChatLinkEvent) {
        switch event {
        case .opened:
            linkOnline = true
        case .closed:
            linkOnline = false
        case .server(let message):
            if message.type == "error", let sessionID = message.sessionID,
               sessionID == activeSessionID || activeSessionID == nil {
                // 优先显示服务端消息，旧服务端只有 code 时提供兜底。
                errorMessage = message.message ?? message.code.map { "服务错误：\($0)" }
            }
            guard message.sessionID == nil || message.sessionID == activeSessionID else { return }
            if message.type == "snapshot", message.sessionID != activeSessionID { return }
            projection.apply(message)
        }
    }

    private func describe(_ error: APIError) -> String {
        switch error {
        case .unauthorized: return "认证失败：请检查主机的 API Key"
        case .unreachable(let reason): return "连不上主机：\(reason)"
        case .invalidResponse(let code): return "主机返回异常（HTTP \(code)）"
        case .decode: return "响应解析失败"
        }
    }
}
