import SwiftUI
import EasyAgentBackend

// MARK: - 三屏骨架：主机、会话列表、对话。iPad 上由 NavigationSplitView 承载（App target 接入）。

public struct HostListView: View {
    @Environment(AppModel.self) private var model
    @State private var addingHost = false

    public init() {}

    public var body: some View {
        List {
            ForEach(model.hosts) { host in
                Button {
                    Task { await model.connect(hostID: host.id) }
                } label: {
                    HStack {
                        VStack(alignment: .leading) {
                            Text(host.name).foregroundStyle(.primary)
                            Text(host.baseURL.absoluteString).font(.caption).foregroundStyle(.secondary)
                        }
                        Spacer()
                        if host.id == model.currentHostID {
                            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                        }
                    }
                }
            }
        }
        .overlay {
            if model.hosts.isEmpty {
                ContentUnavailableView("还没有主机", systemImage: "server.rack",
                                       description: Text("添加一台运行 EasyAgent 的主机，例如你的 Mac 或迷你主机"))
            }
        }
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button("添加主机", systemImage: "plus") { addingHost = true }
            }
        }
        .sheet(isPresented: $addingHost) {
            AddHostSheet()
        }
        .navigationTitle("主机")
        .task { await model.bootstrap() }
    }
}

struct AddHostSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var address = ""
    @State private var apiKey = ""
    @State private var validating = false
    @State private var errorText: String?

    var body: some View {
        NavigationStack {
            Form {
                TextField("名称", text: $name)
                TextField("地址（http://100.64.0.1:8080）", text: $address)
                    .autocorrectionDisabled()
                SecureField("API Key", text: $apiKey)
                    .autocorrectionDisabled()
                if let errorText {
                    Text(errorText).foregroundStyle(.red).font(.caption)
                }
            }
            .navigationTitle("添加主机")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if validating { ProgressView() } else { Button("保存") { save() } }
                }
            }
        }
    }

    private func save() {
        guard let url = URL(string: address.trimmingCharacters(in: .whitespaces)),
              ["http", "https"].contains(url.scheme?.lowercased() ?? "") else {
            errorText = "地址需要是 http(s):// 开头的完整 URL"
            return
        }
        let host = AgentHost(name: name.isEmpty ? url.host ?? "主机" : name, baseURL: url)
        validating = true
        Task {
            do {
                try await keyStoreSave(host: host, key: apiKey)
                await model.add(host: host)
                dismiss()
            } catch {
                errorText = error.localizedDescription
            }
            validating = false
        }
    }

    private func keyStoreSave(host: AgentHost, key: String) async throws {
        let store = SystemKeychainStore()
        try store.saveKey(key, for: host.id)
    }
}

public struct SessionListView: View {
    @Environment(AppModel.self) private var model

    public init() {}

    public var body: some View {
        List {
            ForEach(model.sessions) { session in
                NavigationLink(value: session.id) {
                    row(session)
                }
            }
        }
        .overlay {
            if model.sessions.isEmpty {
                ContentUnavailableView("没有会话", systemImage: "bubble.left.and.bubble.right",
                                       description: Text(model.linkOnline ? "主机上一时会话还没建" : "未连接主机"))
            }
        }
        .refreshable { await model.reloadSessions() }
        .navigationTitle("会话")
        .navigationDestination(for: String.self) { sessionID in
            ChatView(sessionID: sessionID)
        }
    }

    @ViewBuilder
    private func row(_ session: SessionSummary) -> some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(session.title.isEmpty ? "未命名会话" : session.title)
                if !session.runState.isEmpty {
                    Text(stateText(session)).font(.caption).foregroundStyle(.orange)
                }
            }
            Spacer()
        }
    }

    private func stateText(_ session: SessionSummary) -> String {
        session.runState == "running" ? "运行中" : "待审批 \(session.pendingConfirmations)"
    }
}

public struct ChatView: View {
    @Environment(AppModel.self) private var model
    @State private var draft = ""
    private let sessionID: String

    public init(sessionID: String) {
        self.sessionID = sessionID
    }

    public var body: some View {
        VStack(spacing: 0) {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 10) {
                        ForEach(model.projection.entries) { entry in
                            ChatEntryView(entry: entry).id(entry.id)
                        }
                    }
                    .padding()
                }
                .onChange(of: model.projection.entries.last?.id) { _, last in
                    if let last { withAnimation { proxy.scrollTo(last, anchor: .bottom) } }
                }
            }
            Divider()
            HStack(spacing: 8) {
                TextField("输入消息…", text: $draft, axis: .vertical)
                    .textFieldStyle(.roundedBorder)
                    .lineLimit(1...5)
                    .onSubmit { send() }
                Button("发送", systemImage: "paperplane.fill") { send() }
                    .disabled(draft.trimmingCharacters(in: .whitespaces).isEmpty)
            }
            .padding()
        }
        .navigationTitle(model.sessions.first { $0.id == sessionID }?.title ?? "会话")
        .task { await model.open(sessionID: sessionID) }
    }

    private func send() {
        let text = draft
        draft = ""
        Task { await model.send(draft: text) }
    }
}

struct ChatEntryView: View {
    let entry: ChatEntry

    var body: some View {
        switch entry.kind {
        case .user:
            HStack {
                Spacer()
                Text(entry.text)
                    .padding(10)
                    .background(.blue.opacity(0.15), in: RoundedRectangle(cornerRadius: 12))
            }
        case .assistant:
            Text(entry.text.isEmpty ? "…" : entry.text)
                .padding(10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(.quaternary, in: RoundedRectangle(cornerRadius: 12))
        case .thought:
            Label(entry.text.isEmpty ? "…" : entry.text, systemImage: "brain")
                .font(.caption)
                .foregroundStyle(.secondary)
                .padding(8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 8))
        case .tool:
            HStack {
                Image(systemName: entry.toolStatus == "失败" ? "exclamationmark.triangle" : "wrench.and.screwdriver")
                Text("\(entry.toolName) · \(entry.toolStatus)")
                Spacer()
            }
            .font(.caption)
            .padding(8)
            .background(.orange.opacity(0.12), in: RoundedRectangle(cornerRadius: 8))
        }
    }
}
