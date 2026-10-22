package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

type confirmRecorder struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (r *confirmRecorder) server(t *testing.T) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "POST" && req.URL.Path == "/sessions/s1/run/confirm" {
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			r.mu.Lock()
			r.bodies = append(r.bodies, body)
			r.mu.Unlock()
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (r *confirmRecorder) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.bodies) }
func (r *confirmRecorder) last() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[len(r.bodies)-1]
}

func approvalPreview(t *testing.T, rec *confirmRecorder, pending confirmation) (*preview, *ui.Tester) {
	a := alignmentPreview(t)
	a.Live.Client, _ = newBackendClient(rec.server(t).URL, "")
	a.Live.Connected = true
	s := a.current()
	s.RemoteID = "s1"
	s.Confirmations = []confirmation{pending}
	s.Pending = true
	return a, ui.NewTester(a.View, 1240, 820)
}

// File edits can be allowed for the rest of the session; a later edit is
// approved without asking, and the grant can be revoked (#267).
func TestApprovalAllowFilesForSession(t *testing.T) {
	rec := &confirmRecorder{}
	a, tt := approvalPreview(t, rec, confirmation{ID: "c1", ToolName: "edit", Description: "修改 main.go"})
	click(t, tt, "本会话允许所有文件修改")
	waitNative(t, a, func() bool { return rec.count() == 1 })
	if rec.last()["approved"] != true || rec.last()["confirmation_id"] != "c1" {
		t.Fatalf("first approval %+v", rec.last())
	}
	s := a.current()
	waitNative(t, a, func() bool { return !a.Live.Loading["confirm/s1"] })
	s.Confirmations = []confirmation{{ID: "c2", ToolName: "write", Description: "写入 a.txt"}}
	tt.Frame()
	waitNative(t, a, func() bool { return rec.count() == 2 })
	if rec.last()["confirmation_id"] != "c2" || rec.last()["approved"] != true {
		t.Fatalf("session scope not applied: %+v", rec.last())
	}
	if !s.AutoApprove["files"] {
		t.Fatal("scope not recorded")
	}
	s.AutoApprove = nil // the session menu's revoke clears the scope
	waitNative(t, a, func() bool { return !a.Live.Loading["confirm/s1"] })
	s.Confirmations = []confirmation{{ID: "c3", ToolName: "edit", Description: "修改 b.go"}}
	tt.Frame()
	if rec.count() != 2 {
		t.Fatal("revoked scope still approved")
	}
	if _, ok := tt.Find("工具审批"); !ok {
		t.Fatal("approval card not shown after revoking")
	}
}

// Dangerous bash commands only get one-off approval, and the matched rule is shown.
func TestDangerousBashHasNoSessionScope(t *testing.T) {
	rec := &confirmRecorder{}
	_, tt := approvalPreview(t, rec, confirmation{ID: "c1", ToolName: "bash", Description: "即将执行 shell 命令（递归删除）:\n  rm -rf build"})
	if _, ok := tt.Find("本会话允许所有文件修改"); ok {
		t.Fatal("bash offered a session-wide approval")
	}
	if _, ok := tt.Find("本会话允许此工具"); ok {
		t.Fatal("bash offered a session-wide approval")
	}
	if _, ok := tt.Find("审批原因"); !ok {
		t.Fatal("approval reason not shown")
	}
}

// A typed reason is sent with the rejection so the model can adapt.
func TestRejectWithReason(t *testing.T) {
	rec := &confirmRecorder{}
	a, tt := approvalPreview(t, rec, confirmation{ID: "c1", ToolName: "edit", Description: "修改 main.go"})
	a.current().RejectReason = "先别改 main.go"
	click(t, tt, "拒绝")
	waitNative(t, a, func() bool { return rec.count() == 1 })
	if rec.last()["approved"] != false || rec.last()["reason"] != "先别改 main.go" {
		t.Fatalf("rejection %+v", rec.last())
	}
}

// ⌘↩ typed while the composer has focus must not approve anything.
func TestApprovalShortcutNeedsCardFocus(t *testing.T) {
	rec := &confirmRecorder{}
	a, tt := approvalPreview(t, rec, confirmation{ID: "c1", ToolName: "bash", Description: "危险命令"})
	click(t, tt, "输入面板")
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	a.liveTick()
	if rec.count() != 0 {
		t.Fatal("⌘↩ outside the approval card approved a command")
	}
}

func TestApprovalShortcutInsideCard(t *testing.T) {
	rec := &confirmRecorder{}
	a, tt := approvalPreview(t, rec, confirmation{ID: "c1", ToolName: "edit", Description: "修改 main.go"})
	click(t, tt, "拒绝原因")
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	waitNative(t, a, func() bool { return rec.count() == 1 })
	if rec.last()["approved"] != true {
		t.Fatalf("⌘↩ in the card should approve: %+v", rec.last())
	}
}
