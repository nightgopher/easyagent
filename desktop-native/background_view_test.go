package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// The 「后台」 page lists tasks, shows output and stops a running task; the
// session header shows how many run (#244).
func TestBackgroundTasksPage(t *testing.T) {
	var stopped atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		started := time.Now().Add(-90 * time.Second).Format(time.RFC3339Nano)
		switch r.URL.Path {
		case "/sessions/s1/background-tasks":
			running := !stopped.Load()
			status := "running"
			if !running {
				status = "killed"
			}
			_, _ = w.Write([]byte(`{"tasks":[{"id":"bg-1","command":"npm run dev","status":"` + status + `","running":` + map[bool]string{true: "true", false: "false"}[running] + `,"started":"` + started + `"}]}`))
		case "/sessions/s1/background-tasks/bg-1":
			_, _ = w.Write([]byte(`{"task":{"id":"bg-1"},"output":"ready on :5173"}`))
		case "/sessions/s1/background-tasks/bg-1/stop":
			stopped.Store(true)
			_, _ = w.Write([]byte(`{"stopped":true}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	a := alignmentPreview(t)
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.Live.Connected = true
	s := a.current()
	s.RemoteID = "s1"
	a.loadBackgroundTasks(s)
	waitNative(t, a, func() bool { return a.runningBackgroundTasks(s) == 1 })

	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "后台任务")
	if !a.Workbench || a.Live.Workbench.Tab != "background" {
		t.Fatal("header chip does not open the background page")
	}
	tt.Frame()
	if !tt.HasText("npm run dev") {
		t.Fatalf("task not listed: %v", tt.Texts())
	}
	click(t, tt, "查看输出：bg-1")
	waitNative(t, a, func() bool { tt.Frame(); return tt.HasText("ready on :5173") })
	click(t, tt, "停止后台任务：bg-1")
	waitNative(t, a, func() bool { return stopped.Load() && a.runningBackgroundTasks(s) == 0 })
	tt.Frame()
	if _, ok := tt.Find("后台任务"); ok {
		t.Fatal("header chip stays after the task stopped")
	}
}

// A task call becomes a sub-task node: progress lines are kept as steps and
// the final answer renders as Markdown (#244).
func TestSubagentNodeShowsSteps(t *testing.T) {
	a := alignmentPreview(t)
	s := a.current()
	m := &message{Tool: "task", Kind: "tool", Status: "执行中", CallID: "t1", RunID: s.runID(), Args: map[string]any{"description": "查找调用方", "prompt": "list callers"}}
	s.Messages = []*message{{User: true, Text: "找一下"}, m}
	for _, line := range []string{"查找调用方 · step 1 · grep", "查找调用方 · step 1 · grep", "查找调用方 · step 2 · read"} {
		s.event(agentEvent{Type: "tool_update", ToolID: "t1", Partial: line})
	}
	if len(m.Steps) != 2 {
		t.Fatalf("steps %v", m.Steps)
	}
	m.Expanded = true
	tt := ui.NewTester(a.View, 1240, 820)
	if !tt.HasText("子任务") || !tt.HasText("查找调用方") || !tt.HasText("2 步") {
		t.Fatalf("sub-task header: %v", tt.Texts())
	}
	s.event(agentEvent{Type: "tool_end", ToolID: "t1", Result: "Foo 在 **a.go:12** 被调用"})
	tt.Frame()
	if _, open := tt.Find("收起工具结果"); !open {
		if _, ok := tt.Find("工具组：1 个工具调用"); ok {
			click(t, tt, "工具组：1 个工具调用")
		}
	}
	tt.Frame()
	if !tt.HasText("a.go:12") {
		t.Fatalf("answer not rendered: %v", tt.Texts())
	}
}
