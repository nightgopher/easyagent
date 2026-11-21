package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestWorkflowSettingsShowsLiveRunAndConfirmsResume(t *testing.T) {
	a := alignmentPreview(t)
	a.Settings.Open, a.Settings.Tab = true, "workflows"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			requests++
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		if r.URL.Path == "/dynamic-workflows" {
			_ = json.NewEncoder(w).Encode(map[string]any{"available": true, "runs": []any{map[string]any{"id": "dw-test", "name": "调研", "status": "interrupted"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "dw-test", "name": "调研", "status": "interrupted", "state": map[string]any{"nodes": []any{map[string]any{"siteId": "research", "status": "completed"}}}})
	}))
	defer server.Close()
	a.Live.Client, _ = newBackendClient(server.URL, "token")
	a.loadSettingsPage()
	waitNative(t, a, func() bool { return !a.Live.Loading["workflows"] })
	tt := ui.NewTester(a.View, 1080, 1050)
	if !tt.HasText("调研 · 已中断") {
		t.Fatal("missing workflow")
	}
	click(t, tt, "调研 · 已中断")
	waitNative(t, a, func() bool { return !a.Live.Loading["workflow-detail"] })
	tt.Frame()
	if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, tt, filepath.Join(dir, "workflow.png"))
	}
	if !tt.HasText("恢复运行") {
		t.Fatal("missing resume")
	}
	click(t, tt, "恢复运行")
	if requests != 0 || a.Live.Confirm == nil {
		t.Fatal("resume must require confirmation")
	}
}
