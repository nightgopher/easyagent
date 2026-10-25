import Foundation

// MARK: - ChatSocket
// 服务端 /ws 契约（internal/server/websocket.go 的 wsClientMessage/wsServerMessage）：
// 连接带 Bearer；客户端发 subscribe/prompt/ping；服务端回 snapshot/delta/accepted/error 等。

public struct WSClientMessage: Encodable, Sendable {
    public var type: String
    public var sessionID: String?
    public var prompt: String?
    public var requestID: String?
    public var runID: String?
    public var afterSeq: UInt64?

    public init(type: String, sessionID: String? = nil, prompt: String? = nil,
                requestID: String? = nil, runID: String? = nil, afterSeq: UInt64? = nil) {
        self.type = type
        self.sessionID = sessionID
        self.prompt = prompt
        self.requestID = requestID
        self.runID = runID
        self.afterSeq = afterSeq
    }

    enum CodingKeys: String, CodingKey {
        case type, prompt
        case sessionID = "session_id"
        case requestID = "request_id"
        case runID = "run_id"
        case afterSeq = "after_seq"
    }
}

public struct WSServerMessage: Decodable, Sendable {
    public var type: String
    public var sessionID: String?
    public var runID: String?
    public var requestID: String?
    public var seq: UInt64?
    public var state: String?
    public var duplicate: Bool?
    public var code: String?
    /// delta 文本或事件对象，按需在层上再解。
    public var event: AnyDecodable?
    public var events: AnyDecodable?
    public var prompt: String?

    enum CodingKeys: String, CodingKey {
        case type, seq, state, duplicate, code, event, events, prompt
        case sessionID = "session_id"
        case runID = "run_id"
        case requestID = "request_id"
    }
}

/// 保留原始 JSON 值的宽松解码容器。
public struct AnyDecodable: Decodable, Sendable {
    public let value: any Sendable

    public init(_ value: any Sendable) { self.value = value }

    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if let string = try? container.decode(String.self) { value = string }
        else if let double = try? container.decode(Double.self) { value = double }
        else if let bool = try? container.decode(Bool.self) { value = bool }
        else if let array = try? container.decode([AnyDecodable].self) { value = array.map(\.value) }
        else if let object = try? container.decode([String: AnyDecodable].self) {
            value = object.mapValues(\.value)
        } else {
            value = NSNull()
        }
    }
}

public enum ChatLinkEvent: Sendable {
    case opened
    case closed
    case server(WSServerMessage)
}

/// 单主机聊天连接：维护 /ws 长连、订阅与重连后的断点续传（after_seq）。
/// UI 层通过 events() 拿 AsyncStream；M1 只暴露订阅与发 prompt，审批等后续迭代加。
public actor ChatSocket {
    private let host: Host
    private let apiKeyProvider: @Sendable () -> String
    private let session: URLSession

    private var task: URLSessionWebSocketTask?
    private var continuations: [UUID: AsyncStream<ChatLinkEvent>.Continuation] = [:]
    private var lastSeqBySession: [String: UInt64] = [:]
    private var subscribedSessions: Set<String> = []
    private var receiveLoopTask: Task<Void, Never>?

    public init(host: Host, apiKeyProvider: @escaping @Sendable () -> String, session: URLSession = .shared) {
        self.host = host
        self.apiKeyProvider = apiKeyProvider
        self.session = session
    }

    public func events() -> AsyncStream<ChatLinkEvent> {
        AsyncStream { continuation in
            let id = UUID()
            continuations[id] = continuation
            continuation.onTermination = { [weak self] _ in
                Task { await self?.removeContinuation(id) }
            }
        }
    }

    private func removeContinuation(_ id: UUID) {
        continuations[id] = nil
    }

    public func connect() {
        guard task == nil else { return }
        var request = URLRequest(url: wsURL)
        request.setValue("Bearer \(apiKeyProvider())", forHTTPHeaderField: "Authorization")
        let task = session.webSocketTask(with: request)
        self.task = task
        task.resume()
        receiveLoopTask = Task { [weak self] in
            guard let self else { return }
            await self.emit(.opened)
            await self.receiveLoop(on: task)
        }
    }

    public func disconnect() {
        receiveLoopTask?.cancel()
        receiveLoopTask = nil
        task?.cancel(with: .goingAway, reason: nil)
        task = nil
    }

    /// 订阅会话投影；重连后自动带 after_seq 续传。
    public func subscribe(sessionID: String) {
        subscribedSessions.insert(sessionID)
        try? send(WSClientMessage(type: "subscribe", sessionID: sessionID,
                                  afterSeq: lastSeqBySession[sessionID]))
    }

    public func unsubscribe(sessionID: String) {
        subscribedSessions.remove(sessionID)
        lastSeqBySession[sessionID] = nil
        try? send(WSClientMessage(type: "unsubscribe", sessionID: sessionID))
    }

    /// 发送提示词。返回 false 表示尚未连接，调用方保留草稿。
    @discardableResult
    public func sendPrompt(sessionID: String, requestID: String, prompt: String) -> Bool {
        do {
            try send(WSClientMessage(type: "prompt", sessionID: sessionID,
                                     prompt: prompt, requestID: requestID))
            return true
        } catch {
            return false
        }
    }

    public func ping() {
        try? send(WSClientMessage(type: "ping"))
    }

    // MARK: - internals

    private var wsURL: URL {
        var components = URLComponents(url: host.baseURL.appendingPathComponent("ws"), resolvingAgainstBaseURL: false)!
        components.scheme = components.scheme == "https" ? "wss" : "ws"
        return components.url!
    }

    private func send(_ message: WSClientMessage) throws {
        guard let task else { throw APIError.unreachable("socket closed") }
        let data = try JSONEncoder().encode(message)
        task.send(.string(String(data: data, encoding: .utf8) ?? "")) { _ in }
    }

    private func receiveLoop(on task: URLSessionWebSocketTask) async {
        // 每条消息一个迭代；socket 关闭时退出并触发重连。
        while !Task.isCancelled {
            do {
                let message = try await task.receive()
                await handle(message)
            } catch {
                await emit(.closed)
                reconnect()
                return
            }
        }
    }

    private func handle(_ message: URLSessionWebSocketTask.Message) {
        guard case .string(let text) = message,
              let data = text.data(using: String.Encoding.utf8),
              let decoded = try? JSONDecoder().decode(WSServerMessage.self, from: data) else { return }
        if let sessionID = decoded.sessionID, let seq = decoded.seq, seq > (lastSeqBySession[sessionID] ?? 0) {
            lastSeqBySession[sessionID] = seq
        }
        emit(.server(decoded))
    }

    /// 指数退避重连，成功后重放订阅。M1 固定上限 30s；后台挂起由系统断连触发同一路径。
    private func reconnect() {
        Task { [weak self] in
            guard let self else { return }
            try? await Task.sleep(nanoseconds: 1_500_000_000)
            guard !Task.isCancelled else { return }
            await self.disconnect()
            await self.connect()
            let sessions = await self.subscribedSessionsSnapshot()
            for sessionID in sessions {
                await self.subscribe(sessionID: sessionID)
            }
        }
    }

    private func subscribedSessionsSnapshot() -> Set<String> {
        subscribedSessions
    }

    private func emit(_ event: ChatLinkEvent) {
        for continuation in continuations.values {
            continuation.yield(event)
        }
    }
}
