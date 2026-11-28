---
name: desktop-native-ui
description: 修改 desktop-native/ 的 MyGo 原生界面时使用。涵盖控件绑定、布局先例、原生对话框、后台任务、UI 测试与开发实例验证路径。
---

# Desktop Native UI

改 `desktop-native/`（MyGo 原生客户端，无 WebView）前先读本技能，避免重新探索已有的界面范式。

## 先查先例，不发明新写法

- 控件辅助函数在 `desktop-native/settings.go`：`a.input`（标签+输入框，返回输入框元素）、`a.action`/`a.primary`/`a.danger`（按钮）、`a.note`/`a.heading`（文本）。`flatButton`/`iconButton` 在 `view.go`。
- 「标签输入框 + 右侧按钮」一行的既有写法：`ui.Row.Gap(10).AlignItems(ui.End)` 内放 `ui.Column.Grow(1).MinWidth(0)`（包 `a.input`）+ 按钮。先例：`settings_connections.go` 的「项目工作区 + 读取」、`live_services.go` 项目配置行。
- 页面骨架参考同类设置页（如 `business_view.go` 的 sourceEditor）：`heading` → 控件 → `note` 说明 → `businessFooter` 主操作。

## 状态绑定

- MyGo 按指针绑定：`ui.TextInput(c.Key(label), value)` 直接持 `*string`，后台写入状态字段即自动刷新显示，不需要事件回调。
- 表单编辑器模式：编辑前用 seed 函数把数据拷进 `Settings` 的暂存结构（先例 `editKnowledgeSource`），保存时才发请求。

## 原生对话框与后台任务

- 目录/文件选择用 `mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.Window, Directory: true, ButtonLabel: ..., Title: ...})`；保存对话框用 `mygo.Dialog.Save`。
- 可注入测试缝：把选择函数声明为结构体字段（先例 `live.go` 的 `PickDirectory func(mygo.OpenDialogOptions) ([]string, error)`），生产为 nil 时落到 `mygo.Dialog.Open`，测试注入 fake。
- 后台执行用 `a.run(key, work, done)`（`live.go`）：自带 loading key 防重入，host epoch 安全，done 回调回 UI 线程；view 层按钮用 `l.Loading[key]` 置灰。
- 主机边界：原生对话框只能在本机进程弹出。仅 `a.Settings.host().Local` 为真时启用；远程主机禁用按钮、保留文本输入（先例 `workspace_picker.go` 的 openWorkspacePicker 与知识来源 `pickKnowledgeSourceDir`）。

## UI 测试（`*_test.go`，包 main）

- 用 `ui.NewTester(a.View, w, h)` + `click(t, tt, "按钮文字")`；`a.tr` 默认返回原文，中文 label 可直接定位。
- 控件必须在渲染树里才能被找到：先构造状态（含打开面板：`a.Settings.Open = true; a.Settings.Tab = "..."`，编辑器用对应 seed 函数），再建 Tester。点不到控件先查面板是否打开。
- 默认夹具 `newConnectedPreview(t.TempDir())` 是本地主机（Hosts[0] Local: true）；模拟远程主机仿 `workspace_picker_test.go`：`a.Settings.Hosts = append(a.Settings.Hosts[:1], demoHost{ID: "remote", Name: "..."})` + `a.Settings.Host = 1`。
- 异步结果用 `waitNative(t, a, func() bool { ... })` 轮询；httptest 后端仿 `workspacePickerFixture`。

## 验证路径

```bash
cd desktop-native
go run ./tools/prepare                       # 构建本地核心到 resources/darwin-arm64/
EASYAGENT_NATIVE_CORE="$PWD/resources/darwin-arm64/easyagent" go run .   # 开发实例
go vet ./... && go test -count=1 ./...       # 全量验证
```

- 开发实例使用独立 core-data：不碰 `/Applications/EasyAgent.app`、不写真实用户数据；验证涉及写入的操作（保存来源等）前想清楚落点。
- 临时构建产物只进 `build.noindex/`（见根 AGENTS.md 桌面构建收尾）；结束退出开发进程。
- CI 门禁（覆盖率下限、golangci、race）见 `docs/CONTRIBUTING.md`；`make test`/`make vet` 只覆盖根模块，desktop-native 是独立 go 模块需单独验证。
