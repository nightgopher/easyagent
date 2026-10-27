import Foundation
import EasyAgentBackend

// MARK: - 聊天投影
// 对应 mobile-native/backend/Projection.kt：把 snapshot.messages 与 events 流
// 折叠成可渲染的消息列表。M1 覆盖文本与工具卡片骨架，审批/附件后续迭代。

public struct AgentEvent: Codable, Equatable, Sendable {
    public var type: String
    public var textDelta: String?
    public var toolCallID: String?
    public var toolName: String?
    public var isError: Bool?
    public var errorText: String?
    public var summary: String?

    public init(type: String, textDelta: String? = nil, toolCallID: String? = nil,
                toolName: String? = nil, isError: Bool? = nil, errorText: String? = nil,
                summary: String? = nil) {
        self.type = type
        self.textDelta = textDelta
        self.toolCallID = toolCallID
        self.toolName = toolName
        self.isError = isError
        self.errorText = errorText
        self.summary = summary
    }

    enum CodingKeys: String, CodingKey {
        case type, summary
        case textDelta = "text_delta"
        case toolCallID = "tool_call_id"
        case toolName = "tool_name"
        case isError = "is_error"
        case errorText = "error"
    }
}

public struct ChatEntry: Equatable, Identifiable, Sendable {
    public enum Kind: String, Sendable { case user, assistant, thought, tool }

    public var id: UUID = UUID()
    public var kind: Kind
    public var text: String = ""
    public var toolName: String = ""
    public var toolStatus: String = ""
    public var runID: String = ""
    public var callID: String = ""

    public init(kind: Kind, text: String = "", toolName: String = "", toolStatus: String = "",
                runID: String = "", callID: String = "") {
        self.kind = kind
        self.text = text
        self.toolName = toolName
        self.toolStatus = toolStatus
        self.runID = runID
        self.callID = callID
    }
}

public struct ChatProjection: Sendable {
    public private(set) var entries: [ChatEntry] = []
    public private(set) var runState: String = ""

    public init() {}

    /// 处理一条服务端消息：snapshot 重建历史，delta 增量折叠。
    public mutating func apply(_ message: WSServerMessage) {
        switch message.type {
        case "snapshot", "delta", "accepted":
            break
        default:
            return
        }
        if let runID = message.runID, !runID.isEmpty, runID != currentRunID {
            currentRunID = runID
        }
        if let state = message.state, !state.isEmpty {
            runState = state
        }
        if message.type == "snapshot" {
            // snapshot 带完整事件历史；重放即得全量投影。
            entries = []
        }
        for event in decode(message.events) {
            apply(event)
        }
    }

    public mutating func appendUser(_ text: String) {
        entries.append(ChatEntry(kind: .user, text: text))
    }

    // MARK: - internals

    private var currentRunID: String = ""

    private func decode(_ any: AnyDecodable?) -> [AgentEvent] {
        guard let any else { return [] }
        // events 可能是单个对象或数组；投影按数组处理。
        let raw: Any
        switch any.value {
        case let array as [Any]: raw = array
        case let dict as [String: Any]: raw = [dict]
        default: return []
        }
        guard JSONSerialization.isValidJSONObject(raw),
              let data = try? JSONSerialization.data(withJSONObject: raw, options: []) else { return [] }
        return (try? JSONDecoder().decode([AgentEvent].self, from: data)) ?? []
    }

    private mutating func apply(_ event: AgentEvent) {
        switch event.type {
        case "text_delta", "thinking_delta":
            let kind: ChatEntry.Kind = event.type == "thinking_delta" ? .thought : .assistant
            if let last = entries.last, last.kind == kind, last.callID == "",
               (last.runID == currentRunID || last.runID == "") {
                entries[entries.count - 1].text += event.textDelta ?? ""
            } else {
                var entry = ChatEntry(kind: kind, runID: currentRunID)
                if kind == .thought { entry.toolName = "思考过程" }
                entry.text = event.textDelta ?? ""
                entries.append(entry)
            }
        case "tool_start":
            guard !entries.contains(where: { $0.callID == (event.toolCallID ?? "") && $0.kind == .tool && $0.runID == currentRunID }) else { return }
            var entry = ChatEntry(kind: .tool, toolName: event.toolName ?? "工具",
                                  toolStatus: "执行中", runID: currentRunID)
            entry.callID = event.toolCallID ?? ""
            entries.append(entry)
        case "tool_end":
            guard let callID = event.toolCallID,
                  let index = entries.lastIndex(where: { $0.callID == callID && $0.kind == .tool }) else { return }
            entries[index].toolStatus = (event.isError ?? false) ? "失败" : "完成"
        case "error":
            var entry = ChatEntry(kind: .assistant, runID: currentRunID)
            entry.text = event.errorText ?? "出错了"
            entries.append(entry)
        default:
            break
        }
    }
}
