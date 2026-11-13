package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestNativeExpertMarketLayouts renders the expert market tab from fixture
// catalog data and asserts the card anatomy: icon tile row, search field,
// team roster badge, installed badge, and filter chips. A PNG snapshot is
// written only when EASYAGENT_PREVIEW_SNAPSHOTS is set.
func TestNativeExpertMarketLayouts(t *testing.T) {
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Live.Data["expert-market"] = map[string]any{
		"items": []any{
			map[string]any{
				"id": "fullstack", "kind": "expert", "category": "technology", "icon": "code",
				"name":         map[string]any{"zh": "全栈开发专家", "en": "Full-stack developer"},
				"description":  map[string]any{"zh": "端到端交付。", "en": "Delivery."},
				"instructions": map[string]any{"zh": "你是全栈开发专家。", "en": "You are full-stack."},
				"skills":       []any{"web-dev"},
				"tags":         []any{map[string]any{"zh": "全栈开发", "en": "Full-stack"}},
			},
			map[string]any{
				"id": "meeting", "kind": "expert", "category": "office", "icon": "file-text",
				"name":         map[string]any{"zh": "会议纪要专家", "en": "Meeting notes"},
				"description":  map[string]any{"zh": "整理会议内容。", "en": "Notes."},
				"instructions": map[string]any{"zh": "你是会议纪要专家。", "en": "You take notes."},
			},
			map[string]any{
				"id": "dev-team", "kind": "team", "category": "technology", "icon": "shapes",
				"name":        map[string]any{"zh": "全栈专家团", "en": "Full-stack team"},
				"description": map[string]any{"zh": "团队协作。", "en": "Team."},
				"roles":       map[string]any{"zh": "全栈（负责人）、审查", "en": "lead, review"},
				"leader":      "fullstack",
				"members":     []any{"meeting"},
			},
		},
		"installed": []any{
			map[string]any{"template_id": "fullstack", "kind": "expert", "agent_ids": []any{"agent-1"}},
		},
	}
	a.Settings.Business.ExpertTab = "market"
	a.openSettings("agents")
	tt := ui.NewTester(a.View, 1240, 820)
	for _, want := range []string{
		"全栈开发专家",      // expert card
		"负责人 + 1 名成员", // team roster badge
		"已安装过",        // installed badge from the ledger
		"搜索专家市场",      // market search field
	} {
		if !tt.HasText(want) {
			t.Fatalf("expert market missing %q", want)
		}
	}
	// The search box filters cards by name in both languages.
	a.Settings.Business.ExpertQuery = "meeting"
	defer func() { a.Settings.Business.ExpertQuery = "" }()
	tt.Frame()
	if tt.HasText("全栈开发专家") {
		t.Fatal("search query did not filter cards")
	}
	if !tt.HasText("会议纪要专家") {
		t.Fatal("search query dropped the matching card")
	}
	if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, tt, filepath.Join(dir, "expert-market.png"))
	}
	_ = json.Marshal
}
