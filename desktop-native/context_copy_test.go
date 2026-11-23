package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestContextCopiesCompleteSystemPromptAndReportsFailure(t *testing.T) {
	a := alignmentPreview(t)
	a.Workbench = true
	a.Live.Workbench.Tab = "context"
	prompt := "开头\n" + strings.Repeat("A very long prompt line.\n", 600) + "结尾\n"
	a.Live.Data["context"] = map[string]any{"system": prompt, "usage": map[string]any{"system": 123}, "messages": []any{}, "tools": []any{}}
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "系统提示词 · 估算 123 tokens")
	click(t, tt, "复制系统提示词")
	if got := tt.Clipboard(); got != prompt {
		t.Fatalf("clipboard did not preserve the complete prompt: got %d bytes, want %d", len(got), len(prompt))
	}
	if !tt.HasText("已复制系统提示词") {
		t.Fatal("copy success feedback missing")
	}
	tt.SetClipboard("original")
	tt.FailClipboard(errors.New("clipboard unavailable"))
	click(t, tt, "复制系统提示词")
	if tt.Clipboard() != "original" {
		t.Fatal("failed write changed the clipboard")
	}
	if !tt.HasText("复制失败，请检查剪贴板权限") {
		t.Fatal("copy failure feedback missing")
	}
}
