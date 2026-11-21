package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

const workflowFixture = `{"id":"dw-0123456789abcdef01234567","name":"分析发布问题","parent_session_id":"layout","status":"running","graph":{"phases":[{"id":"phase#1","name":"收集信息"},{"id":"phase#2","name":"交叉验证"}],"phaseEdges":[{"from":"phase#1","to":"phase#2"},{"from":"phase#2","to":"phase#1"}],"lanes":[{"id":"actor#1","name":"调研员"},{"id":"actor#2","name":"审查员"}],"steps":[{"id":"ask#1","phase":"phase#1","lane":"actor#1"},{"id":"ask#2","phase":"phase#2","lane":"actor#2"}]},"state":{"actors":[{"siteId":"actor#1","ordinal":0,"name":"调研员","sessionId":"sess_111"},{"siteId":"actor#2","ordinal":0,"name":"审查员","sessionId":"sess_222"}],"nodes":[{"siteId":"ask#1","actorSiteId":"actor#1","actorOrdinal":0,"status":"completed"},{"siteId":"ask#2","actorSiteId":"actor#2","actorOrdinal":0,"status":"running"}]}}`

func workflowFixtureData(t *testing.T) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal([]byte(workflowFixture), &data); err != nil {
		t.Fatal(err)
	}
	return data
}
func TestWorkflowProjectionUsesActualGraphAndActors(t *testing.T) {
	data := workflowFixtureData(t)
	stages, edges := projectWorkflow(data)
	if len(stages) != 2 || stages[0].Name != "收集信息" || stages[0].Status != "completed" || stages[1].Status != "running" {
		t.Fatalf("bad stages: %+v", stages)
	}
	if len(edges) != 2 || edges[1].From != "交叉验证" || edges[1].To != "收集信息" {
		t.Fatalf("loop edge lost: %+v", edges)
	}
	if len(stages[1].Actors) != 1 || stages[1].Actors[0].SessionID != "sess_222" {
		t.Fatalf("actor identity lost: %+v", stages[1])
	}
	data["status"] = "interrupted"
	interrupted, _ := projectWorkflow(data)
	if interrupted[1].Status != "interrupted" || interrupted[1].Actors[0].Status != "interrupted" {
		t.Fatalf("stale running state after interruption: %+v", interrupted[1])
	}
	delete(data, "graph")
	fallback, _ := projectWorkflow(data)
	if len(fallback) != 1 || len(fallback[0].Actors) != 2 {
		t.Fatalf("old run fallback: %+v", fallback)
	}
}
func TestWorkflowCardRemainsVisibleAfterTurnAndOnNarrowWindow(t *testing.T) {
	for _, size := range []struct{ w, h int }{{1240, 820}, {780, 720}} {
		a := alignmentPreview(t)
		s := a.current()
		s.RemoteID = "layout"
		s.Running = false
		s.Messages = []*message{{User: true, Text: "帮我分析"}, {Kind: "thought", Tool: "思考", Text: "先分析"}, {Kind: "tool", Tool: "create_workflow", Status: "完成", Details: map[string]any{"workflow_run_id": "dw-0123456789abcdef01234567"}}, {Text: "分析完成"}}
		a.Live.Data["workflow-card/dw-0123456789abcdef01234567"] = workflowFixtureData(t)
		tt := ui.NewTester(a.View, size.w, size.h)
		for _, text := range []string{"分析发布问题", "收集信息", "交叉验证", "调研员", "审查员"} {
			if !tt.HasText(text) {
				t.Fatalf("%dx%d missing %q", size.w, size.h, text)
			}
		}
		if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
			saveSnapshot(t, tt, filepath.Join(dir, "workflow-conversation.png"))
		}
	}
}
func TestWorkflowIDRequiresActualToolResult(t *testing.T) {
	id := "dw-0123456789abcdef01234567"
	for _, m := range []*message{{Details: map[string]any{"workflow_run_id": id}}, {Result: "工作流 " + id + " · running"}, {Result: `{"run_id":"` + id + `"}`}} {
		if got := conversationWorkflowID(m); got != id {
			t.Fatalf("got %q", got)
		}
	}
	if got := conversationWorkflowID(&message{Result: "title contains no run"}); got != "" {
		t.Fatal(got)
	}
	if got := conversationWorkflowID(&message{Result: "error mentions " + id}); got != "" {
		t.Fatal("error text is not a run identity")
	}
}

func TestWorkflowIDSurvivesFailureAfterProgress(t *testing.T) {
	s := &session{}
	s.event(agentEvent{Type: "tool_start", ToolName: "create_workflow", ToolID: "call"})
	s.event(agentEvent{Type: "tool_update", ToolName: "create_workflow", ToolID: "call", Partial: map[string]any{"content": "工作流 dw-0123456789abcdef01234567 · running"}})
	s.event(agentEvent{Type: "tool_end", ToolName: "create_workflow", ToolID: "call", Result: "运行已取消", Error: true})
	if got := conversationWorkflowID(s.Messages[0]); got != "dw-0123456789abcdef01234567" {
		t.Fatalf("lost run id after failure: %q", got)
	}
}

func TestWorkflowCardLoadsLiveStateAndOpensActor(t *testing.T) {
	var omitted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/dynamic-workflows/dw-0123456789abcdef01234567" {
			if r.URL.Query().Get("omit_graph") == "1" {
				omitted.Store(true)
				var value map[string]any
				_ = json.Unmarshal([]byte(workflowFixture), &value)
				delete(value, "graph")
				value["status"] = "completed"
				_ = json.NewEncoder(w).Encode(value)
				return
			}
			fmt.Fprint(w, workflowFixture)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	a := alignmentPreview(t)
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.Live.Connected = true
	s := a.current()
	s.Messages = []*message{{Kind: "tool", Tool: "create_workflow", Status: "执行中", Result: "工作流 dw-0123456789abcdef01234567 · running"}}
	a.tickConversationWorkflows()
	waitNative(t, a, func() bool { return a.Live.Data["workflow-card/dw-0123456789abcdef01234567"] != nil })
	a.Live.WorkflowNext = time.Time{}
	a.tickConversationWorkflows()
	waitNative(t, a, func() bool {
		return str(object(a.Live.Data["workflow-card/dw-0123456789abcdef01234567"])["status"]) == "completed"
	})
	if !omitted.Load() || object(a.Live.Data["workflow-card/dw-0123456789abcdef01234567"])["graph"] == nil {
		t.Fatal("subsequent refresh must retain the cached graph")
	}
	tt := ui.NewTester(a.View, 1240, 820)
	if !tt.HasText("调研员") {
		t.Fatal("live actor missing")
	}
	click(t, tt, "打开角色会话：调研员")
	if a.current().RemoteID != "sess_111" {
		t.Fatalf("wrong actor session: %q", a.current().RemoteID)
	}
}
