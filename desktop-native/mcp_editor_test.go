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
	"time"

	"github.com/egoist/mygo/ui"
)

func mcpEditorPreview(t *testing.T) *preview {
	t.Helper()
	a := alignmentPreview(t)
	a.Settings.Open, a.Settings.Tab = true, "mcp"
	a.Live.Connected = true
	a.Live.Data["mcp"] = map[string]any{"project_trusted": true, "servers": []any{
		map[string]any{"name": "filesystem", "description": "读取工作区文件与目录", "scope": "user", "transport": "stdio", "state": "connected", "enabled": true, "trusted": true, "tool_count": 12},
		map[string]any{"name": "documentation", "description": "检索文档与 API 参考", "scope": "user", "transport": "http", "state": "connected", "enabled": true, "trusted": true, "tool_count": 6},
		map[string]any{"name": "project-tools", "description": "当前项目的专用工具", "scope": "project", "transport": "http", "state": "disabled", "enabled": false, "trusted": false, "tool_count": 0},
	}}
	return a
}

func TestMCPEditorLayoutAndDraftDismissal(t *testing.T) {
	for _, size := range [][2]int{{640, 560}, {960, 760}, {1240, 820}} {
		for _, theme := range []string{"dark", "light"} {
			t.Run(fmt.Sprintf("%d-%s", size[0], theme), func(t *testing.T) {
				a := mcpEditorPreview(t)
				a.Settings.Preferences.Theme = theme
				tt := ui.NewTester(a.View, size[0], size[1])
				if tt.HasText("项目工作区") || tt.HasText("信任此项目") || tt.HasText("保存配置") {
					t.Fatal("overview includes configuration fields before requested")
				}
				snapshot := func(name string) {
					if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
						saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("mcp-%s-%d-%s.png", name, size[0], theme)))
					}
				}
				snapshot("list")
				click(t, tt, "添加服务")
				assertDialog := func(save string) {
					panel, ok := tt.Find("MCP 配置弹窗")
					if !ok || panel.X < 15 || panel.Y < 15 || panel.X+panel.W > float32(size[0])-15 || panel.Y+panel.H > float32(size[1])-15 {
						t.Fatalf("dialog escaped viewport: %+v", panel)
					}
					for _, label := range []string{"关闭 MCP 配置", "取消", save} {
						control, ok := tt.Find(label)
						if !ok || control.Y < panel.Y || control.Y+control.H > panel.Y+panel.H || control.X+control.W > panel.X+panel.W {
							t.Fatalf("dialog action clipped: %s %+v / %+v", label, control, panel)
						}
					}
				}
				assertDialog("保存配置")
				if tt.HasText("请求头 · KEY=VALUE") || !tt.HasText("表单配置") || !tt.HasText("JSON 导入") {
					t.Fatal("form tabs or progressive fields are incorrect")
				}
				snapshot("form")
				click(t, tt, "更多选项")
				assertDialog("保存配置")
				snapshot("options")
				click(t, tt, "更多选项")
				click(t, tt, "JSON 导入")
				if !a.Settings.Advanced || a.Settings.JSON != "" {
					t.Fatal("empty form should open a blank JSON editor")
				}
				click(t, tt, "表单配置")
				if a.Settings.Advanced {
					t.Fatal("empty JSON editor cannot return to form")
				}
				click(t, tt, "JSON 导入")
				click(t, tt, "标准 MCP 配置")
				payload := `{
  "mcpServers": {
    "web-tools": {
      "url": "https://example.com/mcp",
      "headers": {
        "Authorization": "Bearer fixture-token"
      }
    }
  }
}`
				tt.Type(payload)
				assertDialog("保存配置")
				snapshot("json")
				tt.Key(0, ui.KeyEscape)
				tt.Frame()
				if a.Settings.MCPEditorOpen || a.Settings.JSON != payload {
					t.Fatal("dismissal lost the unsaved JSON draft")
				}
				click(t, tt, "继续编辑")
				if !a.Settings.MCPEditorOpen || a.Settings.JSON != payload {
					t.Fatal("could not resume the draft")
				}
				click(t, tt, "取消")
				if a.Settings.MCPEditorOpen || a.settingsFormDirty() || a.Settings.JSON != "" {
					t.Fatal("Cancel did not discard the draft")
				}
			})
		}
	}
}

func TestMCPEditorSavesOnlySelectedServiceAndKeepsFailures(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			a := mcpEditorPreview(t)
			a.Settings.Workspace = "/project with spaces"
			requests := make(chan bool, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var config map[string]any
				err := json.NewDecoder(r.Body).Decode(&config)
				requests <- err == nil && r.Method == "PUT" && r.URL.Path == "/mcp/servers/selected" && r.URL.Query().Get("scope") == "project" && r.URL.Query().Get("workspace") == "/project with spaces" && config["url"] == "https://example.com/mcp" && config["mcpServers"] == nil && object(config["headers"])["Authorization"] == "Bearer fixture"
				if fail {
					http.Error(w, "fixture save failure", 500)
					return
				}
				fmt.Fprint(w, `{"servers":[{"name":"selected","scope":"project","enabled":true}]}`)
			}))
			defer srv.Close()
			a.Live.Client, _ = newBackendClient(srv.URL, "")
			a.beginMCPEditor(nil)
			a.Settings.Scope, a.Settings.Advanced = "项目", true
			payload := `{"mcpServers":{"other":{"command":"not-run"},"selected":{"url":"https://example.com/mcp","headers":{"Authorization":"Bearer fixture"}}}}`
			a.Settings.JSON = payload
			tt := ui.NewTester(a.View, 960, 760)
			click(t, tt, "导入服务选择")
			click(t, tt, "selected")
			if a.Settings.MCPImportName != "selected" {
				t.Fatal("service selection did not update the import target")
			}
			if len(requests) != 0 {
				t.Fatal("parsing JSON contacted the backend")
			}
			click(t, tt, "保存所选服务")
			select {
			case valid := <-requests:
				if !valid {
					t.Fatal("save lost selected service, configuration, scope, or workspace")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("save never reached backend")
			}
			waitNative(t, a, func() bool { return !a.Live.Loading["mcp-save"] })
			tt.Frame()
			if len(requests) != 0 {
				t.Fatal("unselected service was also saved")
			}
			if fail {
				if !a.Settings.MCPEditorOpen || a.Settings.JSON != payload || a.Live.ResourceErrors["mcp-save"] == "" {
					t.Fatal("failed save lost draft or feedback")
				}
			} else if a.Settings.MCPEditorOpen || a.settingsFormDirty() || !tt.HasText("selected") {
				t.Fatal("successful save did not update list and clear editor")
			}
		})
	}
}

func TestMCPServiceSwitchKeepsScope(t *testing.T) {
	a := mcpEditorPreview(t)
	a.Settings.Workspace = "/project with spaces"
	requests := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Query().Get("scope") != "project" || r.URL.Query().Get("workspace") != a.Settings.Workspace {
			requests <- "wrong scope or method"
			return
		}
		requests <- r.URL.Path
		enabled := strings.HasSuffix(r.URL.Path, "/enable")
		_ = json.NewEncoder(w).Encode(map[string]any{"servers": []any{map[string]any{"name": "project-tools", "scope": "project", "transport": "http", "enabled": enabled}}})
	}))
	defer srv.Close()
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	tt := ui.NewTester(a.View, 960, 760)
	for _, action := range []string{"enable", "disable"} {
		click(t, tt, "启用 MCP 服务：project-tools")
		select {
		case got := <-requests:
			if got != "/mcp/servers/project-tools/"+action {
				t.Fatal("switch changed the wrong service or state", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("switch did not reach the backend")
		}
		waitNative(t, a, func() bool { return !a.Live.Loading["mcp-action"] })
		tt.Frame()
	}
}

func TestMCPEditorValidationIsInline(t *testing.T) {
	a := mcpEditorPreview(t)
	a.beginMCPEditor(nil)
	a.Settings.Advanced, a.Settings.JSON = true, "{\n\"mcpServers\":,\n}"
	tt := ui.NewTester(a.View, 960, 760)
	if !tt.HasText("JSON 第 2 行") {
		t.Fatal("JSON syntax error is not explained beside editor")
	}
	before := a.Settings.JSON
	click(t, tt, "表单配置")
	if !a.Settings.Advanced || a.Settings.JSON != before || !strings.Contains(a.Settings.MCPEditError, "语法错误") {
		t.Fatal("invalid import damaged the draft when switching tabs")
	}
	a.resetMCPEditor()
	a.beginMCPEditor(map[string]any{"name": "existing", "scope": "user", "transport": "http"})
	a.Settings.Advanced, a.Settings.JSON = true, `{"mcpServers":{"different":{"url":"https://example.com/mcp"}}}`
	tt.Frame()
	if !tt.HasText("修改配置时请保留原服务名称") {
		t.Fatal("disabled save has no service-name conflict explanation")
	}
	click(t, tt, "表单配置")
	if !a.Settings.Advanced || a.Settings.MCPName != "existing" {
		t.Fatal("conflicting JSON overwrote the editing identity")
	}
}
