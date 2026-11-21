package main

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestPhoneSessionHomeShowsActiveAndArchivedSessions(t *testing.T) {
	a := alignmentPreview(t)
	a.Sessions = append(a.Sessions, &session{ID: 8, Title: "已归档的会话", Archived: true})
	tt := ui.NewTester(a.phoneSessions, 390, 800)
	if _, ok := tt.Find("当前会话"); !ok {
		t.Fatal("active session is missing")
	}
	if _, ok := tt.Find("已归档的会话"); ok {
		t.Fatal("archived session shown in recent")
	}
	click(t, tt, "已归档")
	if _, ok := tt.Find("已归档的会话"); !ok {
		t.Fatal("archived session is missing")
	}
	if _, ok := tt.Find("当前会话"); ok {
		t.Fatal("active session shown in archive")
	}
	if _, ok := tt.Find("新建会话"); !ok {
		t.Fatal("new session action is missing")
	}
}
