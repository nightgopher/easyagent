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
    private var pumpTask: Task<Void, Never>?

    public init(hostStore: HostStore, keyStore: any APIKeyStore = SystemKeychainStore()) {
        self.hostStore = hostStore
        self.keyStore = keyStore
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
        currentHostID = hostID
        let keyStore = self.keyStore
        let provider: @Sendable () -> String = { keyStore.key(for: hostID) ?? "" }
        client = APIClient(host: host, apiKeyProvider: provider)
        let socket = ChatSocket(host: host, apiKeyProvider: provider)
        self.socket = socket
        projection = ChatProjection()
        await socket.connect()
        await socket.subscribe(sessionID: activeSessionID ?? "")
        startPump(on: socket)
        await reloadSessions()
    }

    public func disconnect() async {
        pumpTask?.cancel()
        pumpTask = nil
        await socket?.disconnect()
        socket = nil
        linkOnline = false
    }

    public func reloadSessions() async {
        guard let client else { return }
        do {
            sessions = try await client.sessions()
            errorMessage = nil
        } catch let error as APIError {
            errorMessage = describe(error)
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    public func open(sessionID: String) async {
        activeSessionID = sessionID
        projection = ChatProjection()
        guard let socket else { return }
        await socket.subscribe(sessionID: sessionID)
    }

    public func send(draft: String) async {
        let trimmed = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, let sessionID = activeSessionID, let socket else { return }
        projection.appendUser(trimmed)
        let requestID = UUID().uuidString
        guard await socket.sendPrompt(sessionID: sessionID, requestID: requestID, prompt: trimmed) else {
            errorMessage = "连接已断开，草稿已保留"
            return
        }
    }

    // MARK: - internals

    private func startPump(on socket: ChatSocket) {
        pumpTask?.cancel()
        pumpTask = Task { [weak self] in
            let stream = await socket.events()
            for await event in stream {
                await MainActor.run {
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
                // 错误消息有 message 字段；WSServerMessage 未建模它，用 code 兜底展示。
                errorMessage = message.code.map { "服务错误：\($0)" }
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
