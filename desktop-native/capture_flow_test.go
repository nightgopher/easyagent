package main

import (
	"encoding/json"
	"fmt"
	"github.com/egoist/mygo/ui"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWorkProcessLifecycleAndSearch(t *testing.T) {
	a := alignmentPreview(t)
	s := a.current()
	s.Messages = []*message{{User: true, Text: "任务"}, {Kind: "thought", Text: "深度思考细节"}, {Text: "先检查代码"}, {Kind: "tool", Tool: "read", Result: "工具证据", Status: "完成"}, {Text: "最终结论"}}
	s.Run = &runInfo{State: "running"}
	s.Running = true
	tt := ui.NewTester(a.View, 1240, 820)
	if !tt.HasText("先检查代码") {
		t.Fatal("running work hidden")
	}
	s.Running = false
	s.Run.State = "completed"
	tt.Frame()
	if tt.HasText("先检查代码") || !tt.HasText("最终结论") {
		t.Fatal("completion did not collapse process and retain answer")
	}
	click(t, tt, "展开或收起工作过程")
	tt.Frame()
	if !tt.HasText("先检查代码") {
		t.Fatal("cannot reopen process")
	}
	click(t, tt, "展开或收起工作过程")
	tt.Frame()
	s.FindOpen = true
	s.FindQuery = "工具证据"
	s.FindJump = true
	tt.Frame()
	if !tt.HasText("工具证据") {
		t.Fatal("search did not reveal nested output")
	}
	s.FindOpen = false
	s.FindQuery = ""
	tt.Frame()
	if tt.HasText("先检查代码") {
		t.Fatal("search overwrote manual collapse")
	}
	if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, tt, filepath.Join(dir, "process-completed.png"))
	}
}
func TestWorkProcessDoesNotHideAbnormalOrMislabelCommentary(t *testing.T) {
	for _, state := range []string{"interrupted", "failed"} {
		a := alignmentPreview(t)
		s := a.current()
		s.Run = &runInfo{State: state}
		s.Messages = []*message{{User: true, Text: "task"}, {Text: "仅仅是中间说明"}, {Kind: "tool", Tool: "bash", Status: "未完成"}}
		p := conversationProcess(s, 1)
		if p.Answer != -1 || !p.Abnormal {
			t.Fatalf("wrong abnormal projection: %+v", p)
		}
		tt := ui.NewTester(a.View, 1240, 820)
		if !tt.HasText("仅仅是中间说明") {
			t.Fatal("abnormal history hidden")
		}
	}
	s := &session{Messages: []*message{{Text: "old"}, {User: true, Text: "new"}, {Text: "next"}}}
	if conversationProcess(s, 0).End != 1 {
		t.Fatal("user input crossed")
	}
}
func TestInboxSaveFailureRetryAndDraftPreservation(t *testing.T) {
	var mu sync.Mutex
	ids := []string{}
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/inbox" {
			var v map[string]string
			_ = json.NewDecoder(r.Body).Decode(&v)
			mu.Lock()
			ids = append(ids, v["id"])
			reject := fail
			fail = false
			mu.Unlock()
			if reject {
				http.Error(w, `{"error":"disk full"}`, 500)
				return
			}
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	a := alignmentPreview(t)
	a.Live.Client, _ = newBackendClient(srv.URL, "test")
	s := a.current()
	s.Draft = "保留记录"
	a.saveDraftToInbox(s)
	waitNative(t, a, func() bool { return !a.Live.Loading["capture-save"] })
	if s.Draft != "保留记录" {
		t.Fatal("failure erased draft")
	}
	a.saveDraftToInbox(s)
	s.Draft = "保存期间的新内容"
	waitNative(t, a, func() bool { return !a.Live.Loading["capture-save"] })
	if s.Draft != "保存期间的新内容" {
		t.Fatal("late response erased newer input")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatal("retry changed idempotency key")
	}
}
func TestCaptureSelectionKnowledgeAndArtifactPreview(t *testing.T) {
	a := alignmentPreview(t)
	s := a.current()
	s.Draft = "原有草稿"
	a.collectMaterial("选中的证据", "session:one")
	tt := ui.NewTester(a.View, 1240, 900)
	click(t, tt, "整理选中材料")
	if !strings.HasPrefix(s.Draft, "原有草稿") || !strings.Contains(s.Draft, "选中的证据") {
		t.Fatal("organize lost draft or source")
	}
	a.referenceKnowledge("docs/lesson.md", "")
	inputs := promptInputMap(s, nil)
	files := inputs["files"].([]map[string]string)
	if len(files) != 1 || files[0]["scope"] != "knowledge" {
		t.Fatalf("knowledge context missing: %v", inputs)
	}
	tt.Frame()
	click(t, tt, "移除引用：docs/lesson.md")
	if len(s.FileReferences) != 0 {
		t.Fatal("cannot remove knowledge reference")
	}
	a.prepareArtifact(s, &message{Text: "# 调研结论\n\n已有能力可以复用。", EntryID: "answer"})
	tt.Frame()
	if !tt.HasText("调研结论") {
		t.Fatal("artifact preview missing")
	}
	if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, tt, filepath.Join(dir, "artifact-preview.png"))
	}
	data := a.captureDraftSnapshot()
	version := a.Live.Store.reserve("capture-draft.json")
	if err := a.Live.Store.save("capture-draft.json", version, data); err != nil {
		t.Fatal(err)
	}
	a.Live.Workbench.Capture = captureWorkbench{}
	a.loadCaptureDraft()
	if a.Live.Workbench.Capture.Artifact == "" || len(a.Live.Workbench.Capture.Selected) != 1 {
		t.Fatal("restart lost drafts")
	}
}
func TestArtifactConflictKeepsEditableDraft(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, `{"error":"file exists"}`, 409) }))
	defer srv.Close()
	a := alignmentPreview(t)
	a.Live.Client, _ = newBackendClient(srv.URL, "test")
	a.prepareArtifact(a.current(), &message{Text: "draft"})
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "保存为 Markdown")
	waitNative(t, a, func() bool { return !a.Live.Loading["artifact-save"] })
	if a.Live.Workbench.Capture.Artifact == "" || a.Live.Error == "" {
		t.Fatal("conflict discarded draft or hid failure")
	}
}

func TestRestoredAbortedHistoryStaysVisible(t *testing.T) {
	s := &session{}
	s.history([]storedMessage{{Role: "user", Content: "task"}, {Role: "assistant", Thinking: "reasoning", Content: "partial", StopReason: "aborted"}, {Role: "user", Content: "next"}, {Role: "assistant", Content: "done", StopReason: "stop"}})
	p := conversationProcess(s, 1)
	if !p.Abnormal || p.End != 3 {
		t.Fatalf("lost aborted history state: %+v", p)
	}
}
