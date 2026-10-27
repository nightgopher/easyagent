package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestMCPSettingsProgressiveDisclosure(t *testing.T) {
	for _, width := range []int{900, 1240} {
		for _, theme := range []string{"dark", "light"} {
			t.Run(fmt.Sprintf("%d-%s", width, theme), func(t *testing.T) {
				a := alignmentPreview(t)
				a.Settings.Open, a.Settings.Tab = true, "mcp"
				a.Settings.Preferences.Theme = theme
				tools := make([]any, 300)
				for i := range tools {
					tools[i] = map[string]any{"name": fmt.Sprintf("read_file_%03d", i), "description": "读取项目文件并返回内容。", "enabled": true}
				}
				a.Live.Data["mcp"] = map[string]any{"servers": []any{
					map[string]any{"name": "filesystem", "scope": "user", "transport": "stdio", "state": "connected", "enabled": true, "trusted": true, "tool_count": 300, "tools": tools},
					map[string]any{"name": "browser", "scope": "project", "transport": "http", "state": "error", "enabled": true, "error": "服务暂时无法连接", "tool_count": 0},
				}}
				tt := ui.NewTester(a.View, width, 820)
				if tt.HasText("read_file_000") || tt.HasText("保存配置") || tt.HasText("修改配置") {
					t.Fatal("details or form shown before requested")
				}
				if !tt.HasText("服务暂时无法连接") {
					t.Fatal("collapsed server hides failure")
				}
				if len(tt.Texts()) > 100 {
					t.Fatal("collapsed servers built tool catalog")
				}
				if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
					saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("mcp-overview-%d-%s.png", width, theme)))
				}
				click(t, tt, "服务详情：filesystem")
				if !tt.HasText("修改配置") || tt.HasText("read_file_000") {
					t.Fatal("server details did not retain separate tool disclosure")
				}
				click(t, tt, "工具列表：filesystem")
				if !tt.HasText("read_file_000") {
					t.Fatal("tools missing after expansion")
				}
				viewport, ok := tt.Find("服务工具：filesystem")
				if !ok || viewport.H > 281 {
					t.Fatalf("unbounded tool list: %+v", viewport)
				}
				click(t, tt, "搜索工具：filesystem")
				tt.Type("299")
				if !tt.HasText("read_file_299") || tt.HasText("read_file_000") {
					t.Fatal("tool search failed")
				}
				if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
					saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("mcp-tools-%d-%s.png", width, theme)))
				}
				tt.Type("-no-match")
				if !tt.HasText("没有匹配的工具，试试其他名称或关键词。") {
					t.Fatal("missing search empty state")
				}
				a.Settings.Workspace = "/another-project"
				tt.Frame()
				if tt.HasText("read_file_299") || tt.HasText("修改配置") {
					t.Fatal("server disclosure leaked across workspaces")
				}
				click(t, tt, "添加服务")
				if !tt.HasText("保存配置") {
					t.Fatal("add form did not open")
				}
			})
		}
	}
}

func TestMCPSettingsToolActionKeepsServerScope(t *testing.T) {
	a := alignmentPreview(t)
	a.Settings.Open, a.Settings.Tab = true, "mcp"
	a.Settings.Workspace = "/project with spaces"
	data := map[string]any{"servers": []any{map[string]any{"name": "files", "scope": "project", "transport": "stdio", "enabled": true, "trusted": true, "tool_count": 1, "tools": []any{map[string]any{"name": "read", "enabled": true}}}}}
	requests := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]bool
		err := json.NewDecoder(r.Body).Decode(&payload)
		_, present := payload["enabled"]
		requests <- err == nil && present && r.Method == "POST" && r.URL.Path == "/mcp/servers/files/tools/read" && r.URL.Query().Get("scope") == "project" && r.URL.Query().Get("workspace") == "/project with spaces" && !payload["enabled"]
		_ = json.NewEncoder(w).Encode(data)
	}))
	defer server.Close()
	a.Live.Client, _ = newBackendClient(server.URL, "")
	a.Live.Data["mcp"] = data
	tt := ui.NewTester(a.View, 900, 820)
	click(t, tt, "服务详情：files")
	click(t, tt, "工具列表：files")
	click(t, tt, "启用工具：read")
	select {
	case ok := <-requests:
		if !ok {
			t.Fatal("tool toggle lost server/project identity or enabled state")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tool toggle did not reach backend")
	}
}
