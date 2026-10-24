package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func filePreviewFixture(t *testing.T) *preview {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/workspace/read-file":
			json.NewEncoder(w).Encode(map[string]any{"content": "# Saved document\n\nOriginal text\n"})
		case "/workspace/list-dir":
			fmt.Fprint(w, `[{"name":"docs","path":"/demo/easyagent/docs","isDir":true},{"name":"README.md","path":"/demo/easyagent/README.md","isDir":false}]`)
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	t.Cleanup(srv.Close)
	a := alignmentPreview(t)
	a.Live.Client, _ = newBackendClient(srv.URL, "test")
	a.Live.Connected = true
	a.Workbench = true
	a.bindFileScope()
	return a
}
func TestNativeFilesEditCancelCloseAndRestore(t *testing.T) {
	a := filePreviewFixture(t)
	path := "/demo/easyagent/README.md"
	a.openNativeFile(path)
	key := a.Live.Workbench.ActiveFile
	waitNative(t, a, func() bool { return !a.Live.Workbench.Files[key].Loading })
	f := a.Live.Workbench.Files[key]
	tt := ui.NewTester(a.View, 1240, 820)
	if !tt.HasText("Saved document") {
		t.Fatal("Markdown file preview missing")
	}
	click(t, tt, "编辑")
	f.Code.Insert("unsaved")
	tt.Frame()
	click(t, tt, "取消编辑")
	if f.Editing || f.Code.Buffer.String() != f.Original || f.Code.Buffer.Modified() {
		t.Fatal("cancel did not restore original")
	}
	f.Scroll.Y = 123
	data := a.fileStateSnapshot()
	version := a.Live.Store.reserve("workspace-files.json")
	if err := a.Live.Store.save("workspace-files.json", version, data); err != nil {
		t.Fatal(err)
	}
	recovered := alignmentPreview(t)
	recovered.Live.Dir = a.Live.Dir
	recovered.loadFileState()
	recovered.Live.Client = a.Live.Client
	recovered.Workbench = true
	recovered.bindFileScope()
	recovered.restoreFileTabs()
	waitNative(t, recovered, func() bool {
		return recovered.Live.Workbench.Files[key] != nil && !recovered.Live.Workbench.Files[key].Loading
	})
	if recovered.Live.Workbench.ActiveFile != key || !recovered.Live.Workbench.Files[key].Preview {
		t.Fatal("file tabs/preview not restored")
	}
	click(t, tt, "关闭标签："+path)
	if a.Live.Workbench.Files[key] != nil {
		t.Fatal("close did not remove tab")
	}
	state := a.fileStateSnapshot()
	var saved map[string]fileWorkspace
	json.Unmarshal(state, &saved)
	if len(saved[a.Live.Workbench.FileScope].Tabs) != 0 {
		t.Fatal("closed tab resurrected")
	}
}
func TestNativeWorkspacePathAndFuzzySearch(t *testing.T) {
	for _, path := range []string{"/demo/easyagent/../secret", "/demo/easyagent/./a", "/demo/easyagent-other/file"} {
		if workspaceContains("/demo/easyagent", path) {
			t.Fatal("restore escaped workspace", path)
		}
	}
	if !workspaceContains(`C:\work`, `C:\work\src\main.go`) {
		t.Fatal("Windows workspace path failed")
	}
	matches := fuzzyPaths([]string{"docs/main.md", "src/main.go", "file.txt"}, "mgo")
	if len(matches) != 1 || matches[0] != "src/main.go" {
		t.Fatal("fuzzy file search failed", matches)
	}
}
func TestNativeContextRedactsLargeImagePayload(t *testing.T) {
	raw := map[string]any{"content": []any{map[string]any{"type": "image", "data": "secret-payload", "mime_type": "image/png"}}}
	redacted := pretty(inspectContext(raw))
	if strings.Contains(redacted, "secret-payload") || !strings.Contains(redacted, "image/png") {
		t.Fatal("image inspection changed metadata or leaked payload", redacted)
	}
	if !strings.Contains(pretty(raw), "secret-payload") {
		t.Fatal("redaction changed model input")
	}
}
func TestNativeStructuredPlanFromTools(t *testing.T) {
	s := &session{Run: &runInfo{ID: "run"}}
	s.event(agentEvent{Type: "tool_start", ToolID: "todo", ToolName: "todo_write", Args: map[string]any{"todos": []any{map[string]any{"content": "检查测试", "status": "in_progress"}}}})
	s.event(agentEvent{Type: "tool_end", ToolID: "todo"})
	if len(s.Plan) != 1 || s.Plan[0].Content != "检查测试" {
		t.Fatal("todo tool not projected", s.Plan)
	}
	a := alignmentPreview(t)
	a.current().Plan = s.Plan
	a.Workbench = true
	a.Live.Workbench.Tab = "plan"
	tt := ui.NewTester(a.View, 1240, 820)
	if !tt.HasText("检查测试") || !tt.HasText("0 / 1 已完成") {
		t.Fatal("structured plan not visible")
	}
}
func TestNativeStreamingMarkdown(t *testing.T) {
	var doc markdownDocument
	source := ""
	stream := true
	tt := ui.NewTester(func(c *ui.Context) { doc.render(c, source, stream) }, 760, 650)
	for _, part := range []string{"## 回", "复\n\n**强调", "内容**\n\n```go\nfunc ready() bool { return true }", "\n```\n\n- [x] 已验证\n\n| 模型 | 状态 |\n|---|---|\n| native | ok |"} {
		source += part
		tt.Frame()
	}
	stream = false
	tt.Frame()
	if !tt.HasText("强调内容") || !tt.HasText("已验证") || !tt.HasText("native") {
		t.Fatal("streaming Markdown not completed")
	}
	click(t, tt, "复制代码")
	if tt.Clipboard() != "func ready() bool { return true }" {
		t.Fatal("code copy lost source", tt.Clipboard())
	}
}
func TestNativeNewParityPanelLayouts(t *testing.T) {
	dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS")
	if dir == "" {
		t.Skip("snapshot opt-in")
	}
	for _, tab := range []string{"files", "review", "context", "profile", "kb", "plan", "tasks"} {
		for _, width := range []int{900, 1240} {
			a := alignmentPreview(t)
			a.Workbench = true
			a.Live.Workbench.Tab = tab
			a.Live.Data["context"] = map[string]any{"system": "System prompt", "messages": []any{map[string]any{"role": "user", "message": map[string]any{"content": "hello"}}}, "tools": []any{map[string]any{"name": "read_file"}}}
			a.Live.Data["profile"] = map[string]any{"total_facts": float64(1), "summary": "**偏好**：中文", "categories": []any{map[string]any{"name": "preferences", "label": "偏好", "facts": []any{map[string]any{"key": "language", "value": "中文", "source": "user", "updated": "2026-10-15"}}}}}
			a.current().Plan = []nativePlanEntry{{Content: "验证布局", Status: "in_progress"}}
			tt := ui.NewTester(a.View, width, 820)
			saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("parity-%s-%d.png", tab, width)))
		}
	}
}

func TestSettingsCopyParity(t *testing.T) {
	// 1. Shared copy constants parity
	sharedConstants := []string{
		SettingCopyGlobalModelConfig,
		SettingCopyDefaultModel,
		SettingCopyCurrentModel,
		SettingCopyModelNote,
		SettingCopyRemoteModelNote,
		SettingCopyRefreshModels,
		SettingCopySaveDefaultModel,
		SettingCopyLocalSource,
		SettingCopyCmdlineMode,
		SettingCopyDesktopMode,
		SettingCopyCmdlineNote,
	}
	for _, sc := range sharedConstants {
		if strings.TrimSpace(sc) == "" {
			t.Fatal("shared copy constant must not be empty")
		}
	}

	// 2. Settings pages registry parity
	for _, p := range settingsPages {
		if p.ID == "skillMarket" {
			t.Fatal("skillMarket should not exist as a separate page in settingsPages registry")
		}
	}
	var capPage *settingsPage
	for i := range settingsPages {
		if settingsPages[i].ID == "capabilities" {
			capPage = &settingsPages[i]
			break
		}
	}
	if capPage == nil {
		t.Fatal("capabilities page missing from settingsPages")
	}
	if capPage.Effect != "新会话生效" {
		t.Fatalf("expected capabilities effect badge to be '新会话生效', got %q", capPage.Effect)
	}

	// 3. Search keywords match capabilities market subview
	for _, q := range []string{"市场", "market", "安装技能", "技能市场"} {
		if !settingsPageMatches(*capPage, q) {
			t.Fatalf("expected query %q to match capabilities page", q)
		}
	}

	// 4. Capabilities subviews navigation
	a := newPreview()
	a.openSettings("capabilities")
	if a.Settings.CapabilityTab != "mine" {
		t.Fatalf("expected default capability tab to be 'mine', got %q", a.Settings.CapabilityTab)
	}
	a.openSettings("skillMarket")
	if a.Settings.Tab != "capabilities" || a.Settings.CapabilityTab != "market" {
		t.Fatalf("expected skillMarket to open capabilities market tab, got tab=%q capTab=%q", a.Settings.Tab, a.Settings.CapabilityTab)
	}
}

func TestSettingsShortcutCaptureAndFooterSync(t *testing.T) {
	a := newPreview()
	if a.shortcutLabel("新会话") != "⌘ N" {
		t.Fatalf("initial new session shortcut should be ⌘ N, got %q", a.shortcutLabel("新会话"))
	}
	tt := ui.NewTester(a.View, 1240, 820)
	if !tt.HasText("⌘ N 新会话") {
		t.Fatal("footer should display initial ⌘ N 新会话")
	}

	// Apply custom shortcut
	if err := a.applyCustomShortcut("new_session", "⌘ 1"); err != nil {
		t.Fatalf("applyCustomShortcut failed: %v", err)
	}
	if a.shortcutLabel("新会话") != "⌘ 1" {
		t.Fatalf("expected new session shortcut to be ⌘ 1, got %q", a.shortcutLabel("新会话"))
	}
	tt.Frame()
	if !tt.HasText("⌘ 1 新会话") {
		t.Fatal("footer should reflect updated shortcut ⌘ 1 新会话")
	}

	// Conflict detection
	err := a.applyCustomShortcut("files", "⌘ 1")
	if err == nil {
		t.Fatal("expected shortcut conflict when assigning same key to files")
	}

	// Reset single shortcut
	a.resetSingleShortcut("new_session")
	if a.shortcutLabel("新会话") != "⌘ N" {
		t.Fatalf("expected new session shortcut to reset to ⌘ N, got %q", a.shortcutLabel("新会话"))
	}
	tt.Frame()
	if !tt.HasText("⌘ N 新会话") {
		t.Fatal("footer should reflect reset shortcut ⌘ N 新会话")
	}
}

func TestSettingsSectionDefaultResetModal(t *testing.T) {
	a := newPreview()

	// Supported sections check
	supported := []string{"general", "appearance", "models", "experiments", "mcp", "feishu"}
	for _, sec := range supported {
		if !a.canResetPageDefaults(sec) {
			t.Fatalf("section %q should support reset page defaults", sec)
		}
	}
	unsupported := []string{"connections", "usage", "capabilities"}
	for _, sec := range unsupported {
		if a.canResetPageDefaults(sec) {
			t.Fatalf("section %q should not support reset page defaults", sec)
		}
	}

	// Test general reset
	a.Settings.Preferences.Language = "English"
	_ = a.applyCustomShortcut("new_session", "⌘ 2")
	a.resetPageDefaults("general")
	if a.Settings.Confirm == nil {
		t.Fatal("expected confirmation modal for general reset")
	}
	if !strings.Contains(a.Settings.Confirm.Title, "常规") || !strings.Contains(a.Settings.Confirm.Description, "界面语言") {
		t.Fatalf("unexpected confirm dialog content: %+v", a.Settings.Confirm)
	}
	a.Settings.Confirm.Action()
	if a.Settings.Preferences.Language != "中文" || a.shortcutLabel("新会话") != "⌘ N" {
		t.Fatal("general reset did not restore default language and shortcuts")
	}

	// Test appearance reset
	a.Settings.Preferences.Theme = "light"
	a.resetPageDefaults("appearance")
	if a.Settings.Confirm == nil {
		t.Fatal("expected confirmation modal for appearance reset")
	}
	a.Settings.Confirm.Action()
	if a.Settings.Preferences.Theme != "dark" {
		t.Fatalf("appearance reset did not restore dark theme, got %q", a.Settings.Preferences.Theme)
	}

	// Test models reset
	a.Settings.Mode = "在桌面客户端配置"
	a.Settings.Model = "custom-model"
	a.Settings.APIKey = "custom-key"
	a.resetPageDefaults("models")
	if a.Settings.Confirm == nil {
		t.Fatal("expected confirmation modal for models reset")
	}
	a.Settings.Confirm.Action()
	if a.Settings.Mode != "沿用命令行配置" || a.Settings.APIKey != "" {
		t.Fatal("models reset did not restore default mode and clear apiKey")
	}
}
