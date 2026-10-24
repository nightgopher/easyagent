package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// refreshSessionSkills asks the host to re-scan the skill directories for this session, so skills
// added after it started apply from the next prompt without a new session.
func (a *preview) refreshSessionSkills() {
	s := a.current()
	if s.RemoteID == "" {
		return
	}
	a.operation("skills-refresh", "POST", sessionPath(s.RemoteID)+"/skills/refresh", map[string]any{}, func(any) { a.refreshWorkbench() })
}

// relativeSkillPath shortens a skill file to a path inside the project for display.
func relativeSkillPath(workspace, source string) string {
	workspace = strings.TrimRight(strings.ReplaceAll(workspace, "\\", "/"), "/")
	source = strings.ReplaceAll(source, "\\", "/")
	if workspace != "" && strings.HasPrefix(source, workspace+"/") {
		return source[len(workspace)+1:]
	}
	return source
}

// skillsWorkbench is the workspace panel's 技能 tab: this project's own skills, and what the session
// actually loaded. Global skills are managed in settings; project skills live with the project, so
// they are shown next to it instead.
func (a *preview) skillsWorkbench(c *ui.Context) {
	l, s := a.Live, a.current()
	if s.RemoteID == "" {
		ui.Column(c).Padding(16).Children(func() { a.note(c, "选择一个项目会话后查看技能。") })
		return
	}
	if a.resourceFeedback(c, "capabilities", a.refreshWorkbench) {
		return
	}
	data := object(l.Data["capabilities"])
	workspace := str(data["workspace"])
	var project, personal []map[string]any
	for _, raw := range list(data["skills"]) {
		skill := object(raw)
		if str(skill["scope"]) == "project" {
			project = append(project, skill)
		} else {
			personal = append(personal, skill)
		}
	}

	ui.Scroll(c).Grow(1).MinHeight(0).Padding(12).Children(func() {
		ui.Column(c).Gap(10).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("本项目技能 · %d", len(project))).FontSize(13).FontWeight(600)
				ui.Spacer(c)
				a.action(c, "刷新技能").Disabled(s.Running || l.Loading["skills-refresh"]).OnClick(a.refreshSessionSkills)
			})
			a.note(c, "读取项目的 .agents/skills，并兼容 .claude/skills。同名时项目里的技能优先于个人技能。在项目里新增或修改技能后，点「刷新技能」即可在本会话使用，不必新建会话；任务运行中需等它结束。")
			if len(project) == 0 {
				a.note(c, "当前项目没有技能。在项目里创建 .agents/skills/<名称>/SKILL.md 后，点「刷新技能」。")
			}
			for _, skill := range project {
				a.projectSkillCard(c, skill, workspace)
			}
			if len(personal) > 0 {
				nativeDisclosure(c, "session-personal-skills", fmt.Sprintf("本会话还加载了 %d 个个人技能", len(personal)), func() {
					for _, skill := range personal {
						ui.Column(c.Key(str(skill["source"]))).Gap(2).Padding(6, 0).Children(func() {
							ui.Text(c, str(skill["name"])).FontSize(12.5).FontWeight(500)
							if d := str(skill["description"]); d != "" {
								ui.Text(c, d).FontSize(11.5).TextColor(c.Theme().TextMuted).MaxLines(2).Tooltip(d)
							}
						})
					}
					a.action(c, "管理个人技能").AlignSelf(ui.Start).OnClick(func() {
						a.Settings.CapabilityTab = "mine"
						a.openSettings("capabilities")
					})
				})
			}
		})
	})
}

func (a *preview) projectSkillCard(c *ui.Context, skill map[string]any, workspace string) {
	name, source := str(skill["name"]), str(skill["source"])
	ui.Column(c.Key(source)).Label("项目技能："+name).Gap(6).Padding(10).Radius(6).Border(1, c.Theme().Border).Children(func() {
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			ui.Text(c, name).FontSize(13).FontWeight(600).SingleLine()
			if origin := str(skill["origin"]); origin == "claude" {
				skillBadge(c, ".claude", false)
			} else if origin != "" {
				skillBadge(c, ".agents", false)
			}
			if !boolean(skill["model_invocation"]) {
				skillBadge(c, "仅显式调用", false)
			}
			if str(skill["overrides"]) != "" {
				skillBadge(c, "覆盖个人同名技能", true)
			}
			ui.Spacer(c)
			a.action(c, "使用").OnClick(func() { a.current().Draft += "\n使用技能 " + name + " " })
			if a.Live.activeHostLocal() {
				a.action(c, "打开文件夹").OnClick(func() { a.showInFileManager(source) })
			}
		})
		if d := str(skill["description"]); d != "" {
			ui.Text(c, d).FontSize(12).LineHeight(1.5).TextColor(c.Theme().TextMuted).MaxLines(3).Tooltip(d)
		}
		ui.Text(c, relativeSkillPath(workspace, source)).Font("monospace").FontSize(11).TextColor(c.Theme().TextMuted).Selectable()
		if o := str(skill["overrides"]); o != "" {
			ui.Text(c, "被它覆盖的个人技能："+o).FontSize(11).TextColor(c.Theme().TextMuted).Selectable()
		}
	})
}
