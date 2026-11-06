package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

const builtinToolsFixture = `{"tools":[
	{"id":"file_read","enabled":true,"effective":"enabled","source":"startup","editable":true},
	{"id":"file_write","enabled":true,"effective":"partial","source":"saved","editable":true,"restricted_by":["blocked_tools"]},
	{"id":"bash","enabled":false,"effective":"disabled","source":"startup","editable":true},
	{"id":"web_search","enabled":true,"effective":"enabled","source":"startup","editable":true},
	{"id":"web_fetch","enabled":true,"effective":"enabled","source":"command","source_detail":"--web","editable":false}
]}`

type builtinTestTransport func(*http.Request) (*http.Response, error)

func (f builtinTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func builtinFixture(t *testing.T) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(builtinToolsFixture), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestBuiltinToolsSettingsLayoutAndUnavailableHost(t *testing.T) {
	for _, width := range []int{900, 1240} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := alignmentPreview(t)
			a.Settings.Open, a.Settings.Tab = true, "permissions"
			a.Live.Data["permissions"] = map[string]any{"default_mode": "ask", "feishu_mode": "full"}
			a.Live.Data["builtin-tools"] = builtinFixture(t)
			tt := ui.NewTester(a.View, width, 1200)
			for _, label := range []string{"内置工具", "联网搜索", "读取网页", "终端命令", "读取文件", "修改文件"} {
				if !tt.HasText(label) {
					t.Fatalf("missing setting %q", label)
				}
			}
			if !tt.HasText("部分可用：受工具过滤配置限制") {
				t.Fatal("partial tool availability was hidden")
			}
			if !tt.HasText("禁用工具名单") || !tt.HasText("--web") {
				t.Fatal("tool restrictions or command-line source were hidden")
			}
			if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
				saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("builtin-tools-%d.png", width)))
			}
			a.Settings.Preferences.Language = "English"
			tt.Frame()
			if !tt.HasText("Built-in tools") || !tt.HasText("Web search") {
				t.Fatal("missing English tool settings")
			}
			a.Settings.Preferences.Language = "中文"
			delete(a.Live.Data, "builtin-tools")
			tt.Frame()
			if !tt.HasText("无法读取工具设置；请连接支持此功能的运行主机。") {
				t.Fatal("unsupported host must not be displayed as disabled")
			}
		})
	}
}

func TestBuiltinToolToggleRequiresSuccessfulHostResponse(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			a := alignmentPreview(t)
			a.Settings.Open, a.Settings.Tab = true, "permissions"
			a.Live.Data["permissions"] = map[string]any{"default_mode": "ask", "feishu_mode": "full"}
			a.Live.Data["builtin-tools"] = builtinFixture(t)
			client, err := newBackendClient("http://127.0.0.1:1", "")
			if err != nil {
				t.Fatal(err)
			}
			client.HTTP.Transport = builtinTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" || r.URL.Path != "/settings/builtin-tools" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var body struct {
					Tool    string `json:"tool"`
					Enabled *bool  `json:"enabled"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Tool != "web_search" || body.Enabled == nil || *body.Enabled {
					t.Errorf("unexpected request body: %+v, %v", body, err)
				}
				if failed {
					return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"disk unavailable"}`))}, nil
				}
				data := builtinFixture(t).(map[string]any)
				for _, raw := range data["tools"].([]any) {
					item := raw.(map[string]any)
					if item["id"] == "web_search" {
						item["enabled"], item["effective"], item["source"] = false, "disabled", "saved"
					}
				}
				encoded, err := json.Marshal(data)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded)))}, err
			})
			a.Live.Client = client
			tt := ui.NewTester(a.View, 1100, 1200)
			click(t, tt, "内置工具：联网搜索")
			waitNative(t, a, func() bool { return !a.Live.Loading["builtin-tools-save"] })
			settings, ok := builtinToolSettings(a.Live.Data["builtin-tools"])
			if !ok {
				t.Fatal("lost tool settings")
			}
			if failed {
				if !boolean(settings["web_search"]["enabled"]) || a.Live.ResourceErrors["builtin-tools-save"] == "" {
					t.Fatal("failed save was shown as successful")
				}
			} else if boolean(settings["web_search"]["enabled"]) {
				t.Fatal("saved toggle was not shown")
			}
		})
	}
}

func TestBuiltinToolSettingsClearOnHostReconnect(t *testing.T) {
	a := hostCredentialPreview(t, &memorySecrets{})
	a.Live.Config.ActiveHost = "remote"
	a.syncSettingsConfig()
	a.Live.Data["builtin-tools"] = builtinFixture(t)
	a.Live.ResourceErrors["builtin-tools"] = "old host read failed"
	a.Live.ResourceErrors["builtin-tools-save"] = "old host save failed"
	a.connectLive()
	if _, ok := a.Live.Data["builtin-tools"]; ok {
		t.Fatal("previous host's tool switches remained visible")
	}
	if a.Live.ResourceErrors["builtin-tools"] != "" || a.Live.ResourceErrors["builtin-tools-save"] != "" {
		t.Fatal("previous host's tool setting error remained visible")
	}
}
