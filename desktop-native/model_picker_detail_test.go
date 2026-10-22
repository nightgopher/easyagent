package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Choices show provider and known context window, and recently chosen models
// come first (#268).
func TestModelPickerDetailsAndRecents(t *testing.T) {
	a := alignmentPreview(t)
	a.Live.Models = []string{"coding", "glm-5.3-flash", "house-model"}
	a.Live.Data["models"] = map[string]any{"models": []any{
		map[string]any{"id": "glm-5.3-flash", "name": "glm-5.3-flash", "provider": "openai", "context_window": float64(128000)},
		map[string]any{"id": "house-model", "name": "House", "provider": "openai"},
	}}
	if got := a.modelChoices(""); got[1].detail() != "openai · 128K 上下文" || got[2].detail() != "openai · house-model" {
		t.Fatalf("details: %q / %q", got[1].detail(), got[2].detail())
	}
	a.Live.rememberModel("house-model")
	a.Live.rememberModel("retired-model") // no longer offered by the host
	if got := a.modelChoices(""); got[0].ID != "house-model" || len(got) != 3 {
		t.Fatalf("recent order: %+v", got)
	}
	a.ModelMenu = true
	a.Live.Loading["model-list"] = true
	tt := ui.NewTester(a.View, 1240, 820)
	if _, ok := tt.Find("正在刷新模型目录"); !ok {
		t.Fatal("refresh hint missing")
	}
	if !tt.HasText("openai · 128K 上下文") {
		t.Fatal("detail line not rendered")
	}
}

func TestFormatWindow(t *testing.T) {
	for tokens, want := range map[int]string{128000: "128K", 200000: "200K", 1000000: "1M", 8192: "8K", 160000: "160K"} {
		if got := formatWindow(tokens); got != want {
			t.Errorf("%d: %s", tokens, got)
		}
	}
}
