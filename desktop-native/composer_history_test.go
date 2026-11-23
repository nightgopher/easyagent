package main

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestComposerHistoryArrowsAndEditing(t *testing.T) {
	a := alignmentPreview(t)
	s := a.current()
	s.Messages = []*message{{User: true, Text: "第一条\n含换行"}, {Text: "回答"}, {User: true, Text: "第二条"}}
	s.Draft = ""
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "消息输入")
	tt.Key(0, ui.KeyUp)
	if s.Draft != "第二条" {
		t.Fatalf("previous=%q", s.Draft)
	}
	tt.Key(0, ui.KeyUp)
	if s.Draft != "第一条\n含换行" {
		t.Fatalf("older=%q", s.Draft)
	}
	tt.Key(0, ui.KeyDown)
	if s.Draft != "第二条" {
		t.Fatalf("newer=%q", s.Draft)
	}
	tt.Key(0, ui.KeyDown)
	if s.Draft != "" {
		t.Fatalf("empty draft not restored: %q", s.Draft)
	}
	tt.Type("第一行")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("第二行")
	original := s.Draft
	tt.Key(0, ui.KeyUp)
	if s.Draft != original {
		t.Fatalf("multiline draft overwritten: %q", s.Draft)
	}
}

func TestComposerHistoryRetainsEditedRecall(t *testing.T) {
	a := alignmentPreview(t)
	s := a.current()
	s.Messages = []*message{{User: true, Text: "old"}, {User: true, Text: "recent"}}
	s.Draft = ""
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "消息输入")
	tt.Key(0, ui.KeyUp)
	tt.Type(" edited")
	edited := s.Draft
	tt.Key(0, ui.KeyUp)
	if s.Draft != edited {
		t.Fatalf("edited history was overwritten: %q", s.Draft)
	}
}

func TestComposerHistoryCaretMovementReturnsToEditing(t *testing.T) {
	a := alignmentPreview(t)
	s := a.current()
	s.Messages = []*message{{User: true, Text: "first"}, {User: true, Text: "first line\nsecond line"}}
	s.Draft = ""
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "消息输入")
	tt.Key(0, ui.KeyUp)
	tt.Key(0, ui.KeyLeft)
	tt.Key(0, ui.KeyUp)
	if s.Draft != "first line\nsecond line" {
		t.Fatalf("caret navigation replaced the recalled draft: %q", s.Draft)
	}
}
func TestComposerHistoryOnlyUsesSubmittedText(t *testing.T) {
	s := &session{Messages: []*message{{Text: "assistant"}, {User: true, Text: ""}, {User: true, Text: "one"}}}
	if got, ok := s.navigateComposerHistory(true, ""); !ok || got != "one" {
		t.Fatalf("unexpected history %q %v", got, ok)
	}
	if _, ok := s.navigateComposerHistory(true, "edited draft"); ok {
		t.Fatal("edited draft should not be overwritten")
	}
	if got, ok := s.navigateComposerHistory(true, ""); !ok || got != "one" {
		t.Fatalf("restart=%q %v", got, ok)
	}
	if got, ok := s.navigateComposerHistory(false, "one"); !ok || got != "" {
		t.Fatalf("restore=%q %v", got, ok)
	}
}
