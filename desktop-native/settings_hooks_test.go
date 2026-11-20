package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestHookSettingsFormSavesAndKeepsDraftOnConflict(t *testing.T) {
	a := alignmentPreview(t)
	a.Settings.Open, a.Settings.Tab = true, "hooks"
	var saved nativeHookDocument
	var conflict atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(map[string]any{"hooks": []nativeHook{}, "revision": "v1"})
			return
		}
		if conflict.Load() {
			http.Error(w, `{"error":"changed"}`, http.StatusConflict)
			return
		}
		var body struct {
			Hooks    []nativeHook `json:"hooks"`
			Revision string       `json:"revision"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Revision != "v1" {
			t.Errorf("unexpected revision %q", body.Revision)
		}
		saved.Hooks = body.Hooks
		_ = json.NewEncoder(w).Encode(map[string]any{"hooks": body.Hooks, "revision": "v2"})
	}))
	defer backend.Close()
	a.Live.Client, _ = newBackendClient(backend.URL, "token")
	a.loadHookSettings(true)
	waitNative(t, a, func() bool { return a.Settings.HookRevision == "v1" })
	tt := ui.NewTester(a.View, 1080, 1050)
	if !tt.HasText("新建 Hook") {
		t.Fatal("missing new hook action")
	}
	click(t, tt, "新建 Hook")
	if !a.Settings.HookFormOpen {
		t.Fatal("new hook form did not open")
	}
	if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
		tt.Frame()
		saveSnapshot(t, tt, filepath.Join(dir, "hooks.png"))
	}
	a.Settings.HookCommand = "/usr/bin/check"
	a.Settings.HookTool = "write"
	a.Settings.HookArgs = "first argument\n--check"
	a.applyHookForm()
	waitNative(t, a, func() bool { return !a.Live.Loading["hooks-save"] })
	if len(saved.Hooks) != 1 || saved.Hooks[0].Command != "/usr/bin/check" || len(saved.Hooks[0].Args) != 2 || saved.Hooks[0].Args[0] != "first argument" {
		t.Fatalf("wrong hook: %+v", saved)
	}
	if a.Settings.HookRevision != "v2" || a.Settings.HookDraft != a.Settings.HookSaved {
		t.Fatal("successful save not acknowledged")
	}
	conflict.Store(true)
	a.editHook(0)
	a.Settings.HookCommand = "/usr/bin/other"
	a.applyHookForm()
	waitNative(t, a, func() bool { return !a.Live.Loading["hooks-save"] })
	if a.Settings.HookDraft == a.Settings.HookSaved || a.hookItems()[0].Command != "/usr/bin/other" {
		t.Fatal("failed save lost draft")
	}
}
