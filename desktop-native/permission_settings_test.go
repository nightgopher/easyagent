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

func TestPermissionDefaultsSettingsLayoutAndSave(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			a := alignmentPreview(t)
			a.Settings.Open, a.Settings.Tab = true, "permissions"
			a.Live.Data["permissions"] = map[string]any{"default_mode": "full", "feishu_mode": "full"}
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/settings/permissions" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body) != 1 || body["feishu_mode"] != "ask" {
					t.Error(body)
				}
				w.Header().Set("Content-Type", "application/json")
				if failed {
					w.WriteHeader(500)
					fmt.Fprint(w, `{"error":"disk unavailable"}`)
					return
				}
				fmt.Fprint(w, `{"default_mode":"ask","feishu_mode":"ask"}`)
			}))
			defer backend.Close()
			a.Live.Client, _ = newBackendClient(backend.URL, "")
			tt := ui.NewTester(a.View, 1000, 900)
			if !tt.HasText("默认工具权限") {
				t.Fatal("permission defaults missing")
			}
			click(t, tt, "默认权限：飞书")
			click(t, tt, "每次询问")
			waitNative(t, a, func() bool { return !a.Live.Loading["permissions-save"] })
			mode := str(object(a.Live.Data["permissions"])["feishu_mode"])
			if failed {
				if mode != "full" || a.Live.ResourceErrors["permissions-save"] == "" {
					t.Fatal("failed save changed displayed permissions")
				}
			} else if mode != "ask" {
				t.Fatal("saved default not shown")
			}
			tt.Frame()
			if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" && !failed {
				saveSnapshot(t, tt, filepath.Join(dir, "permission-defaults.png"))
			}
		})
	}
}

func TestSessionPollingDiscoversFeishuWithoutLosingDraft(t *testing.T) {
	a := alignmentPreview(t)
	local := a.current()
	local.RemoteID, local.Draft = "", "unsent local draft"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/sessions" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		fmt.Fprint(w, `[{"id":"feishu-new","title":"飞书任务","run_state":"waiting_confirmation","pending_confirmations":1}]`)
	}))
	defer backend.Close()
	a.Live.Client, _ = newBackendClient(backend.URL, "")
	a.Live.Connected = true
	a.Live.NextSessionRefresh = time.Now().Add(-time.Second)
	a.liveTick()
	waitNative(t, a, func() bool { return !a.Live.Loading["session-index"] })
	if len(a.Sessions) != 2 || a.current() != local || local.Draft != "unsent local draft" {
		t.Fatal("polling lost the current draft or external session")
	}
	if a.Sessions[1].RemoteID != "feishu-new" {
		t.Fatal("external session missing")
	}
	if !a.Live.NextSessionRefresh.After(time.Now()) {
		t.Fatal("polling not throttled")
	}
}
