package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

const localSkillsKey = "local-skills"

// loadLocalSkills reads every skill in the host's global skill directories. The market's own
// "installed" list only knows skills it installed, so it cannot be used for this view.
func (a *preview) loadLocalSkills() { a.marketRead(localSkillsKey, "/skills/local", nil) }

// refreshSkillLists reloads both lists after a skill is installed or removed: the market view
// marks installed skills from the market list, "my skills" shows the host's whole directory.
func (a *preview) refreshSkillLists() {
	a.loadLocalSkills()
	a.marketRead("installed", "/skills/market/installed", nil)
}

func skillOriginLabel(origin string) string {
	switch origin {
	case "agents":
		return "个人 · .agents"
	case "claude":
		return "个人 · .claude（与 Claude Code 共用）"
	default:
		return "自定义目录"
	}
}

func skillSourceLabel(skill map[string]any) string {
	if boolean(skill["managed"]) {
		return "市场安装"
	}
	switch str(skill["origin"]) {
	case "agents":
		return "个人技能"
	case "claude":
		return "Claude Code"
	default:
		return "自定义目录"
	}
}

type localSkillGroup struct {
	Skill  map[string]any
	Copies []map[string]any
}

// Keep the host's loading precedence, including when its selected copy is disabled.
func groupLocalSkills(skills []any) []localSkillGroup {
	groups := make([]localSkillGroup, 0, len(skills))
	byPath := map[string]int{}
	for _, raw := range skills {
		skill := object(raw)
		if str(skill["shadowed_by"]) == "" {
			byPath[str(skill["path"])] = len(groups)
			groups = append(groups, localSkillGroup{Skill: skill})
		}
	}
	for _, raw := range skills {
		skill := object(raw)
		if winner := str(skill["shadowed_by"]); winner != "" {
			if index, ok := byPath[winner]; ok {
				groups[index].Copies = append(groups[index].Copies, skill)
			} else {
				groups = append(groups, localSkillGroup{Skill: skill})
			}
		}
	}
	return groups
}

func localSkillMatches(skill map[string]any, query string) bool {
	text := str(skill["name"]) + " " + str(skill["description"]) + " " + skillSourceLabel(skill) + " " + skillOriginLabel(str(skill["origin"]))
	return strings.Contains(strings.ToLower(text), query)
}

func skillBadge(c *ui.Context, text string, accent bool) {
	e := ui.Text(c, text).FontSize(11).Padding(1, 8).Radius(20).SingleLine()
	if accent {
		e.Background(c.Theme().Accent.Alpha(.12)).TextColor(c.Theme().Accent)
		return
	}
	e.Border(1, c.Theme().Border).TextColor(c.Theme().TextMuted)
}

// liveLocalSkills is the "我的技能" tab: all global skills on the host, however they got there.
func (a *preview) liveLocalSkills(c *ui.Context) {
	l, s, m := a.Live, &a.Settings, a.marketState()
	data := object(l.Data[localSkillsKey])
	skills := list(data["skills"])

	ui.Row(c).Gap(8).Padding(8, 12).AlignItems(ui.Center).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(6).Children(func() {
		icon(c, "search")
		ui.TextInput(c, &s.LocalSkillQuery).Label("搜索我的技能").Placeholder("搜索技能名称、描述或来源…").Grow(1).MinWidth(0).FontSize(13).Padding(0).Background(ui.Transparent).BorderWidth(0)
	})

	if len(skills) == 0 && l.Loading[localSkillsKey] {
		a.note(c, "正在加载技能…")
		return
	}
	if len(skills) == 0 && data == nil && m.Error != "" {
		ui.Text(c, m.Error).FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
		a.action(c, "重试").AlignSelf(ui.Start).OnClick(a.loadLocalSkills)
		return
	}
	if reason := str(data["installed_error"]); reason != "" {
		ui.Text(c, "无法读取市场安装记录，下面的技能无法区分是否由市场安装，也暂时不能在这里卸载："+reason).FontSize(12).LineHeight(1.5).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
	}
	query := strings.ToLower(strings.TrimSpace(s.LocalSkillQuery))
	groups := groupLocalSkills(skills)
	var shown []localSkillGroup
	enabled := 0
	for _, group := range groups {
		if boolean(group.Skill["enabled"]) && str(group.Skill["shadowed_by"]) == "" {
			enabled++
		}
		matches := localSkillMatches(group.Skill, query)
		for _, copy := range group.Copies {
			matches = matches || localSkillMatches(copy, query)
		}
		if matches {
			shown = append(shown, group)
		}
	}
	if len(groups) > 0 {
		label := fmt.Sprintf("%d 个技能 · %d 个已开启", len(groups), enabled)
		if query != "" {
			label = fmt.Sprintf("找到 %d 个技能", len(shown))
		}
		a.text(c, label).FontSize(11).TextColor(c.Theme().TextMuted).Role(ui.RoleStatus)
	}
	if len(shown) > 0 {
		ui.Column(c).Label("我的技能列表").Gap(0).MinWidth(0).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(8).Children(func() {
			for i, group := range shown {
				if i > 0 {
					ui.Box(c).Height(1).Background(c.Theme().Border)
				}
				a.localSkillRow(c, group.Skill, group.Copies)
			}
		})
	}
	if len(skills) == 0 {
		a.note(c, "主机上还没有技能。可以把含 SKILL.md 的文件夹放进技能目录，或前往「市场」安装。")
		a.action(c, "去技能市场安装").AlignSelf(ui.Start).OnClick(a.openSkillMarket)
	} else if len(shown) == 0 {
		a.note(c, "没有匹配的技能。")
	}
	a.note(c, "开关自动保存，新会话生效；已有会话可在工作区刷新技能。")
	if roots := list(data["roots"]); len(roots) > 0 {
		nativeDisclosure(c, "skill-directories", "技能目录与加载说明", func() {
			for _, raw := range roots {
				ui.Text(c, str(object(raw)["path"])).Font("monospace").FontSize(11).TextColor(c.Theme().TextMuted).Selectable()
			}
			a.note(c, "同名技能优先加载靠前目录中的版本，其他副本可在技能详情中查看。")
		})
	}

	if problems := list(data["problems"]); len(problems) > 0 {
		nativeDisclosure(c, "skill-problems", fmt.Sprintf("%d 条加载提示", len(problems)), func() {
			for _, raw := range problems {
				p := object(raw)
				a.note(c, str(p["message"]))
				nativePre(c, str(p["path"]))
			}
		})
	}
}

func (a *preview) localSkillRow(c *ui.Context, skill map[string]any, copies []map[string]any) {
	l, m := a.Live, a.marketState()
	name, path, dirName := str(skill["name"]), str(skill["path"]), str(skill["dir_name"])
	shadowed := str(skill["shadowed_by"])
	managed := boolean(skill["managed"])
	enabled, supported := skill["enabled"].(bool)
	width, _ := c.Size()
	compact := width < 900
	row := ui.Column(c.Key(l.Config.ActiveHost+"\x00"+path)).Label("技能："+name).Padding(12, 14).Gap(10).MinWidth(0)
	expanded := ui.Local(row, "details", func() bool { return false })
	row.Children(func() {
		ui.Row(c).Gap(14).AlignItems(ui.Center).MinWidth(0).Children(func() {
			details := ui.ButtonBase(c).Label("技能详情："+path).Grow(1).MinWidth(0).Column().AlignItems(ui.Stretch).Gap(4).Padding(2, 0).Radius(4).Justify(ui.Start).Expanded(*expanded).Tooltip("查看技能详情").OnClick(func() { *expanded = !*expanded })
			if details.Hovered() {
				details.Background(c.Theme().SurfaceHover)
			}
			details.Children(func() {
				ui.Row(c).Gap(8).MinWidth(0).Wrap().AlignItems(ui.Center).Children(func() {
					title := ui.Text(c, name).FontSize(13.5).FontWeight(600).MinWidth(0).Shrink(1).SingleLine()
					if shadowed != "" || supported && !enabled {
						title.TextColor(c.Theme().TextMuted)
					}
					if shadowed != "" {
						skillBadge(c, "已被覆盖", false)
					} else if !supported {
						skillBadge(c, "需更新核心", false)
					} else if supported && !enabled {
						skillBadge(c, "已禁用", false)
					}
					if !boolean(skill["model_invocation"]) {
						skillBadge(c, "仅显式调用", false)
					}
				})
				description := strings.Join(strings.Fields(str(skill["description"])), " ")
				if description == "" {
					description = "暂无描述"
				}
				ui.Text(c, description).FontSize(12).LineHeight(1.5).TextColor(c.Theme().TextMuted).SingleLine().Tooltip(description)
				if compact {
					ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() { a.localSkillSource(c, skill, len(copies)) })
				}
			})
			if !compact {
				ui.Column(c).Gap(4).Shrink(0).AlignItems(ui.End).Children(func() { a.localSkillSource(c, skill, len(copies)) })
			}
			if shadowed != "" {
				return
			}
			state := "已关闭"
			if enabled {
				state = "已开启"
			}
			if !supported {
				state = "需更新核心"
			}
			ui.Switch(c, &enabled).Label("启用技能：" + path).Tooltip(state).Shrink(0).Disabled(!supported || l.Loading["skill-toggle"]).OnChange(func() {
				a.operation("skill-toggle", "PUT", "/skills/local", map[string]any{"path": path, "enabled": enabled}, func(value any) {
					l.Data[localSkillsKey] = value
					l.Notice = "技能开关已保存，新会话生效；已有会话请在工作区刷新技能。"
				})
			})
		})
		if !*expanded {
			return
		}
		if d := str(skill["description"]); d != "" {
			ui.Text(c, d).FontSize(12).LineHeight(1.6).TextColor(c.Theme().TextMuted).Selectable()
		}
		ui.Text(c, skillOriginLabel(str(skill["origin"]))).FontSize(11).TextColor(c.Theme().TextMuted)
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			if l.activeHostLocal() {
				a.action(c, "打开文件夹").OnClick(func() { a.showInFileManager(path) })
			}
			switch {
			case managed && m.ConfirmName == dirName:
				marketButton(c, "确认卸载", false).TextColor(c.Theme().Danger).Disabled(l.Loading["uninstall/"+dirName]).OnClick(func() { a.uninstallMarketSkill(dirName, "") })
				marketButton(c, "取消", false).OnClick(func() { m.ConfirmName = "" })
			case managed:
				flatButton(c, "delete", "卸载").FontSize(12.5).Padding(6, 8).TextColor(c.Theme().TextMuted).OnClick(func() { m.ConfirmName = dirName })
			}
		})
		if shadowed != "" {
			ui.Text(c, "同名技能已被更靠前的目录覆盖，这个不会被加载："+shadowed).FontSize(11.5).TextColor(c.Theme().TextMuted)
		}
		ui.Text(c, path).Font("monospace").FontSize(11).TextColor(c.Theme().TextMuted).Selectable()
		if len(copies) > 0 {
			nativeDisclosure(c, "other-copies", fmt.Sprintf("已被覆盖的副本（%d）", len(copies)), func() {
				for _, copy := range copies {
					a.localSkillRow(c, copy, nil)
				}
			})
		}
	})
}

func (a *preview) localSkillSource(c *ui.Context, skill map[string]any, copies int) {
	skillBadge(c, skillSourceLabel(skill), boolean(skill["managed"]))
	if copies > 0 {
		ui.Text(c, fmt.Sprintf("另有 %d 个副本", copies)).FontSize(10.5).TextColor(c.Theme().TextMuted)
	}
}

// openSkillMarket switches to the market tab and loads its first page.
func (a *preview) openSkillMarket() {
	s := &a.Settings
	s.CapabilityTab = "market"
	if s.SkillFilter == "已安装" {
		s.SkillFilter = "推荐"
	}
	a.searchMarket()
}
