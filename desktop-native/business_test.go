package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func businessFixture(t *testing.T) *preview {
	t.Helper()
	a := alignmentPreview(t)
	a.Live.Connected = true
	a.Live.Data["business-agents"] = map[string]any{"agents": []any{map[string]any{"id": "agent-support", "name": "客户反馈助手", "instructions": "整理客户反馈，区分已核实的事实与推测，给出有出处的建议。", "tools": businessDefaultTools, "skills": []string{}, "memory_enabled": true, "version": 1, "knowledge": []nativeKnowledgeBinding{{"team", "write"}}}, map[string]any{"id": "agent-review", "name": "运营复盘助手", "instructions": "汇总周期内的工作成果、问题与改进措施。", "disabled": true, "tools": []string{"read"}}}}
	a.Live.Data["knowledge-sources"] = map[string]any{"sources": []any{map[string]any{"id": "team", "name": "团队操作手册", "path": "/srv/knowledge/team", "directories": []string{"docs", "knowledge"}, "writable": true, "available": true, "version": 1}, map[string]any{"id": "legacy-agent-lessons", "name": "agent-lessons", "path": "/srv/knowledge/agent-lessons", "directories": []string{"knowledge", "docs", "issues"}, "available": true, "version": 1}}}
	a.Live.Data["agent-memories"] = map[string]any{"memories": []nativeAgentMemory{{ID: "memory-one", AgentID: "agent-support", Key: "交付偏好", Content: "每周五发送 PDF 版本的客户反馈总结，注明信息来源。", Scope: "agent", SourceKind: "user", Version: 1, UpdatedAt: "2026-10-31T07:20:00Z"}, {ID: "memory-two", Key: "项目联系窗口", Content: "项目试点期间优先通过邮件联系。", Scope: "workspace", Workspace: "/srv/projects/pilot", Expired: true, ExpiresAt: "2026-10-01T00:00:00Z", Version: 2}}}
	return a
}
func TestBusinessManagementLayouts(t *testing.T) {
	for _, width := range []int{900, 1240} {
		for _, theme := range []string{"dark", "light"} {
			for _, screen := range []string{"agents", "agent-editor", "sources", "source-editor", "memories", "memory-editor", "publication"} {
				t.Run(fmt.Sprintf("%d-%s-%s", width, theme, screen), func(t *testing.T) {
					a := businessFixture(t)
					a.Settings.Open = true
					a.Settings.Preferences.Theme = theme
					a.Settings.Tab = "agents"
					s := &a.Settings.Business
					switch screen {
					case "agent-editor":
						a.editBusinessAgent(a.businessAgents()[0])
					case "sources":
						a.Settings.Tab = "memory-knowledge"
					case "source-editor":
						a.Settings.Tab = "memory-knowledge"
						a.editKnowledgeSource(a.knowledgeSources()[0])
					case "memories", "memory-editor", "publication":
						a.Settings.Tab = "memory-knowledge"
						s.Tab = "memories"
						s.MemoryAgentID = "agent-support"
						m := businessDecode[[]nativeAgentMemory](object(a.Live.Data["agent-memories"])["memories"])[0]
						if screen == "memory-editor" {
							a.editAgentMemory(m)
						}
						if screen == "publication" {
							a.prepareMemoryPublication(m)
						}
					}
					tt := ui.NewTester(a.View, width, 820)
					for _, label := range []string{"创建 Agent", "添加来源", "保存来源", "保存 Agent", "保存记忆", "确认发布"} {
						if box, ok := tt.Find(label); ok && box.W > 190 {
							t.Fatalf("stretched action %s: %+v", label, box)
						}
					}
					if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
						saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("business-%d-%s-%s.png", width, theme, screen)))
					}
				})
			}
		}
	}
}
func TestBusinessDraftGuardAndBoundSessionPayload(t *testing.T) {
	request := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" && r.URL.Path == "/sessions" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			request <- body
			fmt.Fprint(w, `{"id":"created"}`)
		} else if strings.HasSuffix(r.URL.Path, "/info") {
			fmt.Fprint(w, `{"agent_id":"agent-support","workspace":"/role-workspace","model":"role-model"}`)
		} else {
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.Live.Connected = true
	a.current().Workspace = "/unrelated"
	a.current().Model = "unrelated-model"
	a.current().ModelChosen = true
	a.Settings.Open = true
	a.Settings.Tab = "agents"
	a.editBusinessAgent(nativeBusinessAgent{})
	a.Settings.Business.Agent.Name = "Unsaved"
	a.closeSettings()
	if a.Live.Confirm == nil || !a.Settings.Open {
		t.Fatal("dirty draft closed without confirmation")
	}
	a.Live.Confirm.Action()
	a.Live.Confirm = nil
	a.current().AgentID = "agent-support"
	a.newSession()
	waitNative(t, a, func() bool { return a.current().RemoteID == "created" && !a.Live.Loading["create-session"] })
	body := <-request
	if len(body) != 1 || body["agent_id"] != "agent-support" {
		t.Fatalf("role session inherited unrelated workspace or model: %#v", body)
	}
	waitNative(t, a, func() bool { return a.current().Workspace == "/role-workspace" })
	if a.current().AgentID != "agent-support" {
		t.Fatal("identity lost")
	}
}
func TestNativeCoreBusinessAgentLifecycle(t *testing.T) {
	a, _, _, _ := nativeCoreFixture(t)
	a.openSettings("agents")
	waitNative(t, a, func() bool { return !a.Live.Loading["business-agents"] && !a.Live.Loading["knowledge-sources"] })
	a.editKnowledgeSource(nativeKnowledgeSource{Name: "团队手册", Path: t.TempDir(), Writable: true})
	a.saveKnowledgeSource()
	waitNative(t, a, func() bool { return !a.Settings.Business.SourceEditor && !a.Live.Loading["knowledge-sources"] })
	var src nativeKnowledgeSource
	for _, v := range a.knowledgeSources() {
		if v.Name == "团队手册" {
			src = v
		}
	}
	if src.ID == "" {
		t.Fatal("source not saved")
	}
	a.editBusinessAgent(nativeBusinessAgent{})
	a.Settings.Business.Agent.Name = "反馈助手"
	a.Settings.Business.Agent.Instructions = "处理客户反馈。按用户要求保存事实。"
	a.Settings.Business.Agent.Knowledge = []nativeKnowledgeBinding{{src.ID, "write"}}
	a.saveBusinessAgent()
	waitNative(t, a, func() bool { return !a.Settings.Business.AgentEditor && len(a.businessAgents()) == 1 })
	role := a.businessAgents()[0]
	a.startBusinessSession(role.ID)
	waitNative(t, a, func() bool { return a.current().RemoteID != "" && a.current().Workspace != "" })
	first := a.current().RemoteID
	a.current().Draft = "fixture_memory_save 请记住交付偏好"
	a.submitLive()
	waitNative(t, a, func() bool { return a.current().Run != nil && !a.current().Running && !a.current().Sending })
	a.newSession()
	waitNative(t, a, func() bool {
		return a.current().RemoteID != "" && a.current().RemoteID != first && a.current().Workspace != ""
	})
	a.current().Draft = "fixture_memory_search 查询交付偏好"
	a.submitLive()
	waitNative(t, a, func() bool { return a.current().Run != nil && !a.current().Running && !a.current().Sending })
	if !strings.Contains(copyNativeConversation(a.current()), "PDF") {
		t.Fatal("memory was not recalled in a new conversation")
	}
	a.openSettings("memory-knowledge")
	a.Settings.Business.Tab = "memories"
	a.selectMemoryAgent(role.ID)
	waitNative(t, a, func() bool { return !a.Live.Loading["agent-memories"] })
	memories := businessDecode[[]nativeAgentMemory](object(a.Live.Data["agent-memories"])["memories"])
	if len(memories) != 1 || memories[0].SourceSession != first {
		t.Fatalf("missing memory provenance: %+v", memories)
	}
	a.editAgentMemory(memories[0])
	a.Settings.Business.Memory.Content = "改为 Markdown 交付"
	a.saveAgentMemory()
	waitNative(t, a, func() bool { return !a.Settings.Business.MemoryEditor && !a.Live.Loading["agent-memories"] })
	m := businessDecode[[]nativeAgentMemory](object(a.Live.Data["agent-memories"])["memories"])[0]
	if m.Content != "改为 Markdown 交付" {
		t.Fatal("correction missing")
	}
	a.prepareMemoryPublication(m)
	a.publishAgentMemory()
	waitNative(t, a, func() bool { return !a.Settings.Business.Publishing })
	matches, err := filepath.Glob(filepath.Join(src.Path, "*.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("publication: %v %v", matches, err)
	}
	var search map[string]any
	err = a.Live.Client.request(context.Background(), "GET", "/agents/"+role.ID+"/knowledge?q=Markdown", nil, &search)
	if err != nil || len(list(search["entries"])) != 1 {
		t.Fatalf("published knowledge not searchable: %v %#v", err, search)
	}
	a.deleteAgentMemory(m)
	a.Live.Confirm.Action()
	a.Live.Confirm = nil
	waitNative(t, a, func() bool { return !a.Live.Loading["business-save"] && !a.Live.Loading["agent-memories"] })
	if len(list(object(a.Live.Data["agent-memories"])["memories"])) != 0 {
		t.Fatal("delete not reflected")
	}
}

func TestBusinessKnowledgeBindingUsesReadableChoices(t *testing.T) {
	a := businessFixture(t)
	a.Settings.Open = true
	a.Settings.Tab = "agents"
	a.editBusinessAgent(a.businessAgents()[0])
	tt := ui.NewTester(a.View, 1240, 1000)
	click(t, tt, "知识权限：团队操作手册")
	click(t, tt, "只读检索")
	bindings := a.Settings.Business.Agent.Knowledge
	if len(bindings) != 1 || bindings[0].Access != "read" {
		t.Fatalf("permission selection not applied: %+v", bindings)
	}
	click(t, tt, "工具与技能")
	if !tt.HasText("读取工作区文件") {
		t.Fatal("tool selection not revealed")
	}
	click(t, tt, "读取工作区文件")
	for _, name := range a.Settings.Business.Agent.Tools {
		if name == "read" {
			t.Fatal("tool selection not applied")
		}
	}
}
