package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"testing"
)

func longProcessFixture() (*preview, *session, processRange) {
	a := newPreview()
	a.Live = &liveState{Workbench: newNativeWorkbench()}
	s := a.current()
	s.Messages = nil
	for i := 0; i < 200; i++ {
		s.Messages = append(s.Messages, &message{Kind: "tool", Tool: fmt.Sprintf("call-%03d", i), Status: "完成"})
	}
	return a, s, processRange{Start: 0, End: 200, Answer: -1, Tools: 200}
}
func TestLongProcessBuildsRecentStepsAndLoadsOlder(t *testing.T) {
	a, s, p := longProcessFixture()
	tt := ui.NewTester(func(c *ui.Context) { a.processMessages(c, s, p, -1) }, 800, 800)
	if !tt.HasText("call-199") || tt.HasText("call-174") {
		t.Fatal("initial window is not limited to the latest steps")
	}
	if err := tt.Click("显示更早步骤（还有 175 个）"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("call-150") || tt.HasText("call-149") {
		t.Fatal("older steps were not loaded by one page")
	}
}
func TestLongProcessSearchRevealsOldMatchWithoutBuildingMiddle(t *testing.T) {
	a, s, p := longProcessFixture()
	tt := ui.NewTester(func(c *ui.Context) { a.processMessages(c, s, p, 10) }, 800, 800)
	if !tt.HasText("call-010") || !tt.HasText("call-199") || tt.HasText("call-100") {
		t.Fatal("search did not keep the selected step and recent window bounded")
	}
}

func BenchmarkLongProcessRender(b *testing.B) {
	for _, bounded := range []bool{false, true} {
		name := "all"
		if bounded {
			name = "recent"
		}
		b.Run(name, func(b *testing.B) {
			a, s, p := longProcessFixture()
			tt := ui.NewTester(func(c *ui.Context) {
				if bounded {
					a.processMessages(c, s, p, -1)
				} else {
					for i := range s.Messages {
						a.conversationRow(c.Key(i), s, i, -1)
					}
				}
			}, 800, 800)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tt.Frame()
			}
		})
	}
}
