package main

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
)

type nativeTeamMember struct {
	AgentID string `json:"agent_id"`
	Role    string `json:"role"`
}
type nativeTeam struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	LeaderID    string             `json:"leader_id"`
	Members     []nativeTeamMember `json:"members"`
	Workspace   string             `json:"workspace"`
	Disabled    bool               `json:"disabled"`
	Version     int64              `json:"version"`
}
type teamSettingsState struct {
	Editor    bool
	Team      nativeTeam
	Baseline  string
	TeamQuery string
	TeamNote  string
}

func (s *teamSettingsState) snapshot() string {
	if !s.Editor {
		return ""
	}
	b, _ := json.Marshal(s.Team)
	return string(b)
}
func (s *teamSettingsState) dirty() bool {
	return s.Editor && s.snapshot() != s.Baseline
}
func (s *teamSettingsState) closeEditors() {
	s.Editor = false
	s.Baseline = ""
}

// teamNavigate guards leaving a dirty team editor, mirroring businessNavigate.
func (a *preview) teamNavigate(next func()) {
	if a.Live.Loading["business-save"] {
		return
	}
	if a.Settings.Teams.dirty() {
		a.Live.Confirm = &nativeConfirmation{"放弃未保存的修改？", "当前团队修改尚未保存，继续将丢弃这些修改。", func() { a.Settings.Teams.closeEditors(); next() }}
		return
	}
	next()
}

func (a *preview) teams() []nativeTeam {
	return businessDecode[[]nativeTeam](object(a.Live.Data["business-teams"])["teams"])
}
func (a *preview) refreshTeams() {
	a.resource("business-teams", "/teams")
	a.refreshBusiness()
}

// memberAvailability joins a team against the current agent list so the UI can
// flag members that were deleted or disabled after the team was saved.
func (a *preview) memberAvailability(id string) string {
	for _, v := range a.businessAgents() {
		if v.ID == id {
			if v.Disabled {
				return "disabled"
			}
			return "ok"
		}
	}
	return "missing"
}

func (a *preview) editTeam(v nativeTeam) {
	delete(a.Live.ResourceErrors, "business-save")
	s := &a.Settings.Teams
	s.closeEditors()
	s.Editor = true
	if v.Members == nil {
		v.Members = []nativeTeamMember{}
	}
	s.Team = v
	s.Baseline = s.snapshot()
}
func (a *preview) teamMemberIndex(agentID string) int {
	s := &a.Settings.Teams
	return slices.IndexFunc(s.Team.Members, func(m nativeTeamMember) bool { return m.AgentID == agentID })
}
func (a *preview) toggleTeamMember(agentID string, join bool) {
	s := &a.Settings.Teams
	if join {
		if a.teamMemberIndex(agentID) < 0 && agentID != s.Team.LeaderID {
			s.Team.Members = append(s.Team.Members, nativeTeamMember{AgentID: agentID})
		}
		return
	}
	if i := a.teamMemberIndex(agentID); i >= 0 {
		s.Team.Members = slices.Delete(s.Team.Members, i, 1)
	}
}
func (a *preview) teamMemberRole(agentID, role string) {
	if i := a.teamMemberIndex(agentID); i >= 0 {
		s := &a.Settings.Teams
		s.Team.Members[i].Role = role
	}
}
func (a *preview) saveTeam() {
	s := &a.Settings.Teams
	v := s.Team
	method, path := "POST", "/teams"
	if v.ID != "" {
		method, path = "PUT", "/teams/"+url.PathEscape(v.ID)
	}
	a.operation("business-save", method, path, v, func(any) {
		s.closeEditors()
		a.Settings.message("团队已保存；配置在下次启动会话时生效。")
		a.refreshTeams()
	})
}

// teamValid reports whether the draft can be saved: name, leader and at least
// one member that is not the leader.
func (a *preview) teamValid() bool {
	s := &a.Settings.Teams
	if strings.TrimSpace(s.Team.Name) == "" || s.Team.LeaderID == "" || len(s.Team.Members) == 0 {
		return false
	}
	return true
}

// startTeamSession opens a session where the team leader coordinates members.
func (a *preview) startTeamSession(t nativeTeam) {
	if a.Live.configBusy() {
		a.Live.Error = "正在应用连接配置，请稍后创建会话"
		return
	}
	if a.Live.Loading["create-session"] {
		return
	}
	a.Live.Versions["sessions"]++
	a.operation("create-session", "POST", "/sessions", map[string]any{"team_id": t.ID}, func(v any) {
		id := str(object(v)["id"])
		if id == "" {
			return
		}
		a.closeWork()
		a.Live.Versions["sessions"]++
		s := &session{ID: a.NextID, RemoteID: id, Title: "新会话", UpdatedAt: sessionActivityTime(object(v)), Application: "business", AgentID: t.LeaderID, TeamID: t.ID, TeamName: t.Name}
		a.NextID++
		a.Sessions = append([]*session{s}, a.Sessions...)
		a.Active = s.ID
		a.Archived = false
		a.Search = ""
		a.loadSession(s)
		a.ReturnToComposer = true
		a.closeSettings()
	})
}

// memberStatuses derives each member's latest delegation state from the
// conversation: members never delegated to show as waiting.
func memberStatuses(s *session) map[string]string {
	statuses := map[string]string{}
	for _, m := range s.Messages {
		if m.Kind != "tool" || m.Tool != "team_delegate" {
			continue
		}
		ref := str(object(m.Args)["member"])
		if ref == "" {
			continue
		}
		switch m.Status {
		case "完成":
			statuses[ref] = "done"
		case "失败":
			statuses[ref] = "failed"
		case "已拒绝":
			statuses[ref] = "declined"
		default:
			statuses[ref] = "running"
		}
	}
	return statuses
}
