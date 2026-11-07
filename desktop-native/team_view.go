package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// teamMemberStatusLabels maps delegation states to roster labels.
var teamMemberStatusLabels = map[string]string{
	"done": "已完成", "running": "执行中", "failed": "失败", "declined": "已拒绝",
}

// teamFooter mirrors businessFooter for the team editor, including its own
// dirty-draft guard on cancel.
func (a *preview) teamFooter(c *ui.Context, label string, valid bool, save func()) {
	ui.Row(c).Wrap().Gap(8).Padding(14, 0, 0, 0).Children(func() {
		a.primary(c, label).Disabled(!valid || !a.Live.Connected || a.Live.Loading["business-save"]).OnClick(save)
		a.action(c, "取消").Disabled(a.Live.Loading["business-save"]).OnClick(func() { a.teamNavigate(func() { a.Settings.Teams.closeEditors() }) })
	})
	if err := a.Live.ResourceErrors["business-save"]; err != "" {
		ui.Text(c, err).FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
		a.note(c, "输入已保留。若版本冲突，请取消编辑并刷新后重新修改。")
	}
}

// teamRoster renders the frozen team of the current session with each member's
// latest delegation state; members never delegated to show as waiting.
func (a *preview) teamRoster(c *ui.Context, s *session) {
	if s.TeamID == "" || a.Live == nil {
		return
	}
	var team *nativeTeam
	for i := range a.teams() {
		if v := a.teams()[i]; v.ID == s.TeamID {
			team = &v
			break
		}
	}
	if team == nil {
		return
	}
	statuses := memberStatuses(s)
	nativeDisclosure(c, "team-roster", fmt.Sprintf("团队：%s · 成员（%d）", team.Name, len(team.Members)), func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "负责人："+a.businessAgentName(team.LeaderID)+"（统筹）").FontSize(11).TextColor(c.Theme().TextMuted)
			for _, m := range team.Members {
				name := a.businessAgentName(m.AgentID)
				state := statuses[m.AgentID]
				if state == "" {
					state = statuses[name]
				}
				label := "等待委派"
				if text := teamMemberStatusLabels[state]; text != "" {
					label = text
				}
				ui.Row(c.Key(m.AgentID)).Gap(8).Children(func() {
					ui.Text(c, name).FontSize(11)
					if m.Role != "" {
						ui.Text(c, m.Role).FontSize(11).TextColor(c.Theme().TextMuted).Grow(1).MinWidth(0)
					} else {
						ui.Spacer(c)
					}
					ui.Text(c, label).FontSize(11).TextColor(c.Theme().TextMuted)
				})
			}
		})
	})
}

func (a *preview) teamSettings(c *ui.Context) {
	if a.Live == nil {
		a.note(c, "连接运行主机后，可把多个业务 Agent 组成团队协作。")
		return
	}
	s := &a.Settings.Teams
	if s.Editor {
		a.teamEditor(c)
		return
	}
	ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		ui.TextInput(c, &s.TeamQuery).Label("搜索团队").Placeholder("搜索名称或说明…").Grow(1).Basis(180).MinWidth(0)
		a.primary(c, "创建团队").Disabled(!a.Live.Connected).OnClick(func() { a.editTeam(nativeTeam{}) })
		a.action(c, "刷新").Disabled(a.Live.Loading["business-teams"]).OnClick(a.refreshTeams)
	})
	if a.resourceFeedback(c, "business-teams", a.refreshTeams) {
		return
	}
	teams := a.teams()
	agents := a.businessAgents()
	count := 0
	for _, v := range teams {
		if q := strings.ToLower(strings.TrimSpace(s.TeamQuery)); q != "" && !strings.Contains(strings.ToLower(v.Name+" "+v.Description), q) {
			continue
		}
		count++
		unavailable := 0
		for _, m := range v.Members {
			if a.memberAvailability(m.AgentID) != "ok" {
				unavailable++
			}
		}
		ui.Column(c.Key(v.ID)).Label("团队："+v.Name).Padding(16, 0).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
			ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, v.Name).FontSize(15).FontWeight(600).Grow(1).MinWidth(0)
				if v.Disabled {
					a.businessBadge(c, "已停用")
				}
				a.action(c, "编辑").Label("编辑团队：" + v.Name).OnClick(func() { a.editTeam(v) })
				startLabel := "启动团队：" + v.Name
				a.primary(c, "启动团队").Label(startLabel).Disabled(v.Disabled || !a.Live.Connected || a.Live.Loading["create-session"]).OnClick(func() { a.startTeamSession(v) })
			})
			if v.Description != "" {
				ui.Text(c, v.Description).FontSize(12).TextColor(c.Theme().TextMuted).MaxLines(2).LineHeight(1.6)
			}
			ui.Row(c).Wrap().Gap(8).Children(func() {
				a.businessBadge(c, "负责人："+a.businessAgentName(v.LeaderID))
				for _, m := range v.Members {
					label := a.businessAgentName(m.AgentID)
					switch a.memberAvailability(m.AgentID) {
					case "disabled":
						label += "（已停用）"
					case "missing":
						label += "（不存在）"
					}
					a.businessBadge(c, label)
				}
				if len(v.Workspace) > 0 {
					a.businessBadge(c, "共享工作区")
				}
			})
			if unavailable > 0 {
				a.note(c, fmt.Sprintf("有 %d 名成员当前不可用；启动时会被跳过，负责人也会被告知。", unavailable))
			}
		})
	}
	if count == 0 {
		if len(teams) == 0 {
			a.heading(c, "组建一个专家团")
			a.note(c, "选择一名负责人和多名成员。会话中负责人按需把任务委派给成员，各成员使用自己的角色、工具与记忆。")
		} else {
			a.note(c, "没有匹配的团队。")
		}
	}
	if len(agents) == 0 {
		a.note(c, "先在“业务 Agent”页创建至少两名 Agent，再回来组建团队。")
	}
}

func (a *preview) teamEditor(c *ui.Context) {
	s := &a.Settings.Teams
	v := &s.Team
	title := "创建团队"
	if v.ID != "" {
		title = "编辑团队"
	}
	a.heading(c, title)
	a.input(c, "团队名称", &v.Name, "例如：调研小组", false)
	a.multiline(c, "团队说明", &v.Description, 2, 6)
	leaderChoices := []businessChoice{}
	for _, agent := range a.businessAgents() {
		if !agent.Disabled {
			leaderChoices = append(leaderChoices, businessChoice{ID: agent.ID, Label: agent.Name})
		}
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "负责人").FontSize(12).MinWidth(72)
		a.businessSelect(c, "选择负责人", &v.LeaderID, leaderChoices)
	})
	a.note(c, "负责人负责统筹并在需要时委派成员；启动团队会话即授权它使用 team_delegate 委派工具。")

	a.heading(c, "成员")
	joined := 0
	for _, agent := range a.businessAgents() {
		if agent.ID == v.LeaderID || agent.Disabled {
			continue
		}
		index := a.teamMemberIndex(agent.ID)
		if index >= 0 {
			joined++
		}
		ui.Column(c.Key(agent.ID)).Gap(6).Padding(8, 0).Children(func() {
			on := index >= 0
			if ui.Checkbox(c.Key("join"), &on, agent.Name).Changed() {
				a.toggleTeamMember(agent.ID, on)
			}
			if index >= 0 {
				ui.TextInput(c, &v.Members[index].Role).Label("成员 " + agent.Name + " 的分工").Placeholder("例如：负责资料检索（可留空）").FontSize(12)
			}
		})
	}
	if joined == 0 {
		a.note(c, "至少选择一名成员。成员从业务 Agent 中选择，执行时使用各自的角色、工具、知识授权与记忆。")
	}

	a.heading(c, "工作区")
	a.input(c, "团队共享工作区（可选）", &v.Workspace, "留空则成员使用自己的工作区", false)
	a.note(c, "填写后成员的文件访问都限制在该目录（只读）；留空时各成员只能访问自己的工作区。")
	ui.Checkbox(c, &v.Disabled, a.tr("停用团队"))
	a.teamFooter(c, "保存团队", a.teamValid(), a.saveTeam)
}
