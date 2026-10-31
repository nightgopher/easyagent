package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestToolDiscoverySettingsLayoutAndUnknownHost(t *testing.T) {
	for _, width := range []int{900, 1240} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := alignmentPreview(t)
			a.Settings.Open = true
			a.Settings.Tab = "models"
			a.Live.Data["tool-discovery"] = map[string]any{"mode": "local"}
			tt := ui.NewTester(a.View, width, 900)
			if !tt.HasText("按需加载") || !tt.HasText("工具加载") {
				t.Fatal("missing tool loading row")
			}
			rect, ok := tt.Find("工具加载模式")
			if !ok || rect.W > 220 {
				t.Fatalf("tool loading control too wide: %+v", rect)
			}
			if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
				saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("tool-discovery-%d.png", width)))
			}
			a.Settings.Preferences.Language = "English"
			tt.Frame()
			if !tt.HasText("On demand") || !tt.HasText("Tool loading") {
				t.Fatal("missing English loading choices")
			}
			a.Settings.Preferences.Language = "中文"
			delete(a.Live.Data, "tool-discovery")
			tt.Frame()
			if !tt.HasText("无法读取工具加载设置；旧版运行主机需要更新后使用。") {
				t.Fatal("unknown host reported as off")
			}
		})
	}
}

func TestToolDiscoverySaveAcknowledgedByCurrentHost(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			a := alignmentPreview(t)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/settings/tool-discovery" || r.Method != "POST" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["mode"] != "local" {
					t.Error("wrong mode", payload)
				}
				w.Header().Set("Content-Type", "application/json")
				if failed {
					w.WriteHeader(500)
					fmt.Fprint(w, `{"error":"disk unavailable"}`)
					return
				}
				fmt.Fprint(w, `{"mode":"local","revision":2,"effective_from":"next_run"}`)
			}))
			defer backend.Close()
			client, err := newBackendClient(backend.URL, "")
			if err != nil {
				t.Fatal(err)
			}
			a.Live.Client = client
			a.Live.Data["tool-discovery"] = map[string]any{"mode": "off"}
			a.saveToolDiscovery("local")
			waitNative(t, a, func() bool { return !a.Live.Loading["tool-discovery-save"] })
			choice, _ := toolDiscoveryChoice(a.Live.Data["tool-discovery"])
			if failed {
				if choice != "完整加载" || a.Live.ResourceErrors["tool-discovery-save"] == "" {
					t.Fatal("failed save was reported as successful")
				}
			} else if choice != "按需加载" {
				t.Fatal("saved choice not shown")
			}
		})
	}
}

func TestMCPAlwaysLoadSurvivesFormJSONRoundTrip(t *testing.T) {
	s := newSettings()
	s.mcpForm = mcpForm{Scope: "个人", MCPName: "files", Transport: "http", URL: "https://example.com/mcp", AlwaysLoad: true}
	config, err := s.mcpConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config["alwaysLoad"] != true {
		t.Fatal("missing alwaysLoad")
	}
	data, _ := json.Marshal(config)
	s.JSON = string(data)
	if err = s.importMCPToForm(); err != nil {
		t.Fatal(err)
	}
	if !s.AlwaysLoad {
		t.Fatal("JSON import lost loading policy")
	}
}
