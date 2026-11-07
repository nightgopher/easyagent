package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// fakeTransport serves fixture responses in-process so team tests do not
// depend on loopback HTTP, which some hosts filter for test binaries.
type fakeTransport struct {
	handler func(*http.Request) *http.Response
}

func (t fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.handler(r), nil
}

func jsonResp(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func fakeBackend(handler func(*http.Request) *http.Response) *backendClient {
	client, _ := newBackendClient("http://backend", "")
	client.HTTP = &http.Client{Transport: fakeTransport{handler}}
	return client
}

func teamFixture(t *testing.T) *preview {
	t.Helper()
	a := alignmentPreview(t)
	a.Live.Connected = true
	a.Live.Data["business-agents"] = map[string]any{"agents": []any{
		map[string]any{"id": "agent-lead", "name": "调研负责人", "instructions": "统筹调研任务并汇总结果。", "tools": businessDefaultTools, "skills": []string{}, "version": 1},
		map[string]any{"id": "agent-search", "name": "资料检索", "instructions": "检索并整理资料。", "tools": businessDefaultTools, "skills": []string{}, "version": 1},
		map[string]any{"id": "agent-review", "name": "代码审查", "instructions": "审查代码变更。", "tools": businessDefaultTools, "skills": []string{}, "version": 1},
		map[string]any{"id": "agent-off", "name": "已停用角色", "instructions": "不再参与。", "disabled": true, "tools": businessDefaultTools, "skills": []string{}, "version": 1},
	}}
	a.Live.Data["business-teams"] = map[string]any{"teams": []any{
		map[string]any{"id": "team-research", "name": "调研小组", "description": "检索与审查协作。", "leader_id": "agent-lead", "members": []any{map[string]any{"agent_id": "agent-search", "role": "资料检索"}, map[string]any{"agent_id": "agent-ghost", "role": ""}}, "version": 3},
	}}
	return a
}

func TestTeamManagementLayouts(t *testing.T) {
	for _, width := range []int{900, 1240} {
		for _, theme := range []string{"dark", "light"} {
			for _, screen := range []string{"teams", "team-editor"} {
				t.Run(fmt.Sprintf("%d-%s-%s", width, theme, screen), func(t *testing.T) {
					a := teamFixture(t)
					a.Settings.Open = true
					a.Settings.Preferences.Theme = theme
					a.Settings.Tab = "teams"
					if screen == "team-editor" {
						a.editTeam(a.teams()[0])
					}
					tt := ui.NewTester(a.View, width, 820)
					for _, label := range []string{"创建团队", "保存团队", "启动团队"} {
						if box, ok := tt.Find(label); ok && box.W > 190 {
							t.Fatalf("stretched action %s: %+v", label, box)
						}
					}
					if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
						saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("team-%d-%s-%s.png", width, theme, screen)))
					}
				})
			}
		}
	}
}

func TestTeamListFlagsUnavailableMembers(t *testing.T) {
	a := teamFixture(t)
	a.Settings.Open = true
	a.Settings.Tab = "teams"
	tt := ui.NewTester(a.View, 1240, 820)
	for _, want := range []string{"负责人：调研负责人", "agent-ghost（不存在）", "当前不可用"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in team list", want)
		}
	}
	// A fresh draft without members is invalid until one joins.
	a.editTeam(nativeTeam{})
	a.Settings.Teams.Team.Name = "新团队"
	a.Settings.Teams.Team.LeaderID = "agent-lead"
	if a.teamValid() {
		t.Fatal("draft without members should be invalid")
	}
	a.toggleTeamMember("agent-review", true)
	if !a.teamValid() {
		t.Fatal("draft with one member should be valid")
	}
}

func TestTeamEditorToggleAndSavePayload(t *testing.T) {
	var mu sync.Mutex
	var saved []map[string]any
	a := teamFixture(t)
	a.Live.Client = fakeBackend(func(r *http.Request) *http.Response {
		if r.Method == "POST" && r.URL.Path == "/teams" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			saved = append(saved, body)
			mu.Unlock()
			return jsonResp(`{"id":"team-new","name":"调研小组","version":1}`)
		}
		return jsonResp(`{}`)
	})
	a.Settings.Open = true
	a.Settings.Tab = "teams"
	a.editTeam(nativeTeam{})
	s := &a.Settings.Teams
	s.Team.Name = "调研小组"
	s.Team.Description = "检索与审查"
	s.Team.LeaderID = "agent-lead"
	a.toggleTeamMember("agent-search", true)
	a.toggleTeamMember("agent-review", true)
	a.teamMemberRole("agent-review", "代码审查")
	if !a.teamValid() {
		t.Fatal("draft should be valid")
	}
	a.saveTeam()
	waitNative(t, a, func() bool { return !a.Settings.Teams.Editor && !a.Live.Loading["business-save"] })
	mu.Lock()
	defer mu.Unlock()
	if len(saved) != 1 {
		t.Fatalf("expected one save request, got %d", len(saved))
	}
	body := saved[0]
	if body["leader_id"] != "agent-lead" || body["name"] != "调研小组" {
		t.Fatalf("unexpected save payload: %#v", body)
	}
	members, _ := json.Marshal(body["members"])
	if string(members) != `[{"agent_id":"agent-search","role":""},{"agent_id":"agent-review","role":"代码审查"}]` {
		t.Fatalf("unexpected members payload: %s", members)
	}
}

func TestTeamEditorDirtyGuard(t *testing.T) {
	a := teamFixture(t)
	a.Settings.Open = true
	a.Settings.Tab = "teams"
	a.editTeam(a.teams()[0])
	a.Settings.Teams.Team.Name = "改名未保存"
	if !a.settingsFormDirty() {
		t.Fatal("team editor should mark the settings dirty")
	}
	a.closeSettings()
	if a.Live.Confirm == nil || !a.Settings.Open {
		t.Fatal("dirty team draft closed without confirmation")
	}
	a.Live.Confirm.Action()
	a.Live.Confirm = nil
	if a.Settings.Teams.Editor {
		t.Fatal("confirmation should discard the team draft")
	}
}

func TestTeamSessionStartPayload(t *testing.T) {
	var mu sync.Mutex
	var created []map[string]any
	a := teamFixture(t)
	a.Live.Client = fakeBackend(func(r *http.Request) *http.Response {
		if r.Method == "POST" && r.URL.Path == "/sessions" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			created = append(created, body)
			mu.Unlock()
			return jsonResp(`{"id":"team-session"}`)
		}
		return jsonResp(`{}`)
	})
	a.Settings.Open = true
	a.Settings.Tab = "teams"
	a.startTeamSession(a.teams()[0])
	waitNative(t, a, func() bool { return a.current().RemoteID == "team-session" && !a.Live.Loading["create-session"] })
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 1 {
		t.Fatalf("expected one create request, got %d", len(created))
	}
	body := created[0]
	if len(body) != 1 || body["team_id"] != "team-research" {
		t.Fatalf("team session leaked unrelated fields: %#v", body)
	}
	if a.current().TeamID != "team-research" || a.current().TeamName != "调研小组" {
		t.Fatalf("session lost team identity: %+v", a.current())
	}
}

func TestTeamRosterShowsMemberStates(t *testing.T) {
	a := teamFixture(t)
	s := a.current()
	s.TeamID, s.TeamName = "team-research", "调研小组"
	s.Messages = []*message{
		{Kind: "tool", Tool: "team_delegate", CallID: "c1", Status: "完成", Args: map[string]any{"member": "agent-search", "description": "检索资料"}},
		{Kind: "tool", Tool: "team_delegate", CallID: "c2", Status: "执行中", Args: map[string]any{"member": "资料检索", "description": "补充检索"}},
	}
	statuses := memberStatuses(s)
	if statuses["agent-search"] != "done" || statuses["资料检索"] != "running" {
		t.Fatalf("unexpected member statuses: %#v", statuses)
	}
	a.Workbench = false
	tt := ui.NewTester(a.View, 1240, 820)
	roster := "团队：调研小组 · 成员（2）"
	if !tt.HasText(roster) {
		t.Fatal("roster header missing")
	}
	click(t, tt, roster)
	for _, want := range []string{"负责人：调研负责人", "已完成", "等待委派"} {
		if !tt.HasText(want) {
			t.Fatalf("roster missing %q", want)
		}
	}
}
