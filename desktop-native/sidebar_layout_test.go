package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// One or two sessions list directly under their project, without date
// headings or counts of one; many sessions keep the date grouping (#269).
func TestSidebarHidesHeadingsForFewSessions(t *testing.T) {
	a := alignmentPreview(t)
	tt := ui.NewTester(a.View, 1240, 820)
	if _, ok := tt.Find("折叠时间分组：今天"); ok {
		t.Fatal("a single session still sits under a date heading")
	}
	for _, text := range tt.Texts() {
		if text == "1" {
			t.Fatal("sidebar still shows a count of one")
		}
	}
	if _, ok := tt.Find("当前会话"); !ok {
		t.Fatal("session missing from sidebar")
	}

	now := time.Now().Format(time.RFC3339)
	for i := 0; i < sidebarBucketThreshold; i++ {
		a.Sessions = append(a.Sessions, &session{ID: 100 + i, Title: fmt.Sprint("会话 ", i), UpdatedAt: now, Workspace: "/demo/easyagent", Project: "easyagent"})
	}
	tt.Frame()
	if _, ok := tt.Find("折叠时间分组：今天"); !ok {
		t.Fatal("many sessions lost their date grouping")
	}
}
