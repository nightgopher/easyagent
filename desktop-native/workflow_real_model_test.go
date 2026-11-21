package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// This opt-in test calls a real paid model. Credentials are supplied only via
// the environment and every application/data/workspace directory is temporary.
func TestRealModelWorkflowThroughDesktop(t *testing.T) {
	if os.Getenv("EASYAGENT_REAL_WORKFLOW_QA") != "1" {
		t.Skip("explicit real-model QA only")
	}
	for _, name := range []string{"EASYAGENT_NATIVE_CORE", "EA_BASE_URL", "EA_API_KEY"} {
		if os.Getenv(name) == "" {
			t.Fatalf("%s is required", name)
		}
	}
	root := t.TempDir()
	t.Setenv("EA_PROVIDER", "openai")
	t.Setenv("EA_MODEL", "glm-5.3-flash")
	t.Setenv("EA_WORKSPACE", filepath.Join(root, "workspace"))
	t.Setenv("EA_ENV_FILE", filepath.Join(root, "absent.env"))
	t.Setenv("EA_ENABLE_BASH", "0")
	t.Setenv("EA_ENABLE_WEB", "0")
	t.Setenv("EA_ENABLE_WEB_SEARCH", "0")
	t.Setenv("EA_SKILL_MARKET", "0")
	t.Setenv("EA_KB_REPO_PATH", filepath.Join(root, "knowledge"))
	if err := os.MkdirAll(filepath.Join(root, "workspace"), 0700); err != nil {
		t.Fatal(err)
	}
	a := newConnectedPreview(filepath.Join(root, "desktop"))
	a.Live.Secrets = &memorySecrets{}
	defer a.closeLive()
	a.connectLive()
	waitNative(t, a, func() bool { return a.Live.Connected && a.Live.Client != nil })
	a.createLiveSession("")
	waitNative(t, a, func() bool { return a.current().RemoteID != "" })
	s := a.current()
	s.Draft = "/workflow 请让两个角色并行工作：甲用一句中文说明 2+3 的结果，乙用一句中文说明 3×4 的结果，最后合并为简短答案。请明确使用 phase 标记阶段并调用 create_workflow；只做心算，不访问文件或网络。"
	a.submitLive()
	deadline := time.Now().Add(4 * time.Minute)
	var workflow *message
	for time.Now().Before(deadline) {
		a.liveTick()
		for _, m := range s.Messages {
			if m.Tool == "create_workflow" {
				workflow = m
			}
		}
		if s.Run != nil && !s.Running && !s.Pending && (s.Run.State == "completed" || s.Run.State == "failed" || s.Run.State == "cancelled" || s.Run.State == "interrupted") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s.Run == nil || s.Running {
		t.Fatalf("real-model run did not finish; error=%s", a.Live.Error)
	}
	if s.Run.State != "completed" {
		t.Fatalf("real-model run state=%s error=%s", s.Run.State, s.Run.Error)
	}
	if workflow == nil {
		var parts []string
		for _, m := range s.Messages {
			if m.Tool != "" {
				parts = append(parts, m.Tool)
			}
		}
		t.Fatalf("model did not call create_workflow; tools=%v", parts)
	}
	id := conversationWorkflowID(workflow)
	if id == "" {
		t.Fatalf("workflow id missing; tool status=%s result=%s", workflow.Status, workflow.Result)
	}
	a.Live.WorkflowNext = time.Time{}
	for time.Now().Before(deadline) {
		a.liveTick()
		data := object(a.Live.Data["workflow-card/"+id])
		if str(data["status"]) != "" && str(data["status"]) != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	data := object(a.Live.Data["workflow-card/"+id])
	state := object(data["state"])
	graph := object(data["graph"])
	if str(data["status"]) != "completed" {
		t.Fatalf("workflow status=%q error=%v tool=%s", str(data["status"]), data["error"], workflow.Result)
	}
	if len(list(state["actors"])) < 2 || len(list(state["nodes"])) < 2 || len(list(graph["steps"])) < 2 {
		t.Fatalf("missing real graph/roles: actors=%d nodes=%d steps=%d", len(list(state["actors"])), len(list(state["nodes"])), len(list(graph["steps"])))
	}
	tt := ui.NewTester(a.View, 1240, 820)
	if _, ok := tt.Find("工作流卡片：" + str(data["name"])); !ok {
		t.Fatal("workflow card was not rendered in the conversation")
	}
	stages, _ := projectWorkflow(data)
	if len(stages) == 0 || !tt.HasText(stages[0].Name) {
		t.Fatal("the real workflow phase was not shown on the card")
	}
	if !strings.Contains(workflow.Result, "completed") {
		t.Fatalf("tool did not report success: %s", workflow.Result)
	}
	var answer string
	for _, m := range s.Messages {
		if !m.User && m.Kind == "" && m.Tool == "" && strings.TrimSpace(m.Text) != "" {
			answer = m.Text
		}
	}
	if !(strings.Contains(answer, "5") || strings.Contains(answer, "五")) || !(strings.Contains(answer, "12") || strings.Contains(answer, "十二")) {
		t.Fatalf("final answer did not contain both expected results: %q", answer)
	}
	if dir := os.Getenv("EASYAGENT_REAL_WORKFLOW_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, tt, filepath.Join(dir, "real-workflow.png"))
	}
	t.Logf("model=glm-5.3-flash workflow=completed actors=%d nodes=%d steps=%d phases=%d card=visible", len(list(state["actors"])), len(list(state["nodes"])), len(list(graph["steps"])), len(list(graph["phases"])))
}
