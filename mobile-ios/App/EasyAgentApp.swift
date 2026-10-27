import SwiftUI
import EasyAgentBackend
import EasyAgentUI

@main
struct EasyAgentApp: App {
    @State private var model = AppModel(hostStore: HostStore())

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
        }
    }
}

/// iPhone 单栏 / iPad 双栏自适应入口。
struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        NavigationSplitView(columnVisibility: .constant(.all)) {
            HostListView()
        } detail: {
            SessionListView()
        }
        .navigationSplitViewStyle(.balanced)
    }
}
