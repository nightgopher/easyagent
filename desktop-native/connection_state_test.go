package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Without a host the session must not claim to be ready, and the empty state
// explains why sending is unavailable (#264).
func TestDisconnectedSessionSaysSo(t *testing.T) {
	a := alignmentPreview(t)
	tt := ui.NewTester(a.View, 1240, 820)
	if tt.HasText("就绪") || !tt.HasText("未连接") {
		t.Fatalf("status should follow the connection: %v", tt.Texts())
	}
	a.current().Messages = nil
	tt.Frame()
	if _, ok := tt.Find("未连接提示"); !ok {
		t.Fatal("empty state does not explain the missing connection")
	}
	if tt.HasText("梳理一个想法") {
		t.Fatal("suggestions offered although nothing can be sent")
	}
	click(t, tt, "管理运行主机")
	if !a.Settings.Open {
		t.Fatal("hint does not lead to host settings")
	}

	a = alignmentPreview(t)
	a.Live.Connected = true
	tt = ui.NewTester(a.View, 1240, 820)
	if !tt.HasText("就绪") {
		t.Fatal("connected idle session should be ready")
	}
}

// Context usage appears only when reported, warns near the limit, and the
// slash entry says what it does (#266).
func TestComposerToolbarLabels(t *testing.T) {
	a := alignmentPreview(t)
	a.Live.Connected = true
	tt := ui.NewTester(a.View, 1240, 820)
	if _, ok := tt.Find("上下文占用"); ok {
		t.Fatal("unknown context usage should not render a dead control")
	}
	if !tt.HasText("/ 命令") {
		t.Fatal("slash entry has no visible meaning")
	}
	a.current().Usage = map[string]any{"context_window": float64(200000), "estimated_tokens": float64(170000)}
	tt.Frame()
	if _, ok := tt.Find("上下文占用"); !ok || !tt.HasText("85%") {
		t.Fatalf("usage not shown: %v", tt.Texts())
	}
}
