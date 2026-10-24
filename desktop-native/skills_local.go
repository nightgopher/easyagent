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

	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		icon(c, "search")
		ui.TextInput(c, &s.LocalSkillQuery).Label("搜索我的技能").Placeholder("搜索技能名称或描述…").Grow(1).MinWidth(0).FontSize(13).Padding(8, 12).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(6)
	})
	a.note(c, "主机上全局技能目录里的全部技能，不论是手写的、其他工具装的还是从市场安装的。项目目录里的技能在工作区面板的「技能」里看。文件变更后，新建会话或在那里点「刷新技能」即可生效。")

	if len(skills) == 0 && l.Loading[localSkillsKey] {
		a.note(c, "正在加载技能…")
		return
	}
	if len(skills) == 0 && data == nil && m.Error != "" {
		ui.Text(c, m.Error).FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
		a.action(c, "重试").AlignSelf(ui.Start).OnClick(a.loadLocalSkills)
		return
	}
	roots := list(data["roots"])
	if len(roots) > 0 {
		paths := make([]string, 0, len(roots))
		for _, raw := range roots {
			paths = append(paths, str(object(raw)["path"]))
		}
		ui.Text(c, "目录："+strings.Join(paths, "  ·  ")).Font("monospace").FontSize(11).TextColor(c.Theme().TextMuted).Selectable()
	}

	query := strings.ToLower(strings.TrimSpace(s.LocalSkillQuery))
	shown := 0
	for _, raw := range skills {
		skill := object(raw)
		if query != "" && !strings.Contains(strings.ToLower(str(skill["name"])+" "+str(skill["description"])), query) {
			continue
		}
		shown++
		a.localSkillRow(c, skill)
	}
	if len(skills) == 0 {
		a.note(c, "主机上还没有技能。可以把含 SKILL.md 的文件夹放进上面的目录，或前往「市场」安装。")
		a.action(c, "去技能市场安装").AlignSelf(ui.Start).OnClick(a.openSkillMarket)
	} else if shown == 0 {
		a.note(c, "没有匹配的技能。")
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

func (a *preview) localSkillRow(c *ui.Context, skill map[string]any) {
	l, m := a.Live, a.marketState()
	name, path, dirName := str(skill["name"]), str(skill["path"]), str(skill["dir_name"])
	shadowed := str(skill["shadowed_by"])
	managed := boolean(skill["managed"])
	ui.Column(c.Key(path)).Label("技能："+name).Padding(12, 0).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			title := ui.Text(c, name).FontSize(13.5).FontWeight(600).SingleLine()
			if shadowed != "" {
				title.TextColor(c.Theme().TextMuted)
			}
			skillBadge(c, skillOriginLabel(str(skill["origin"])), false)
			if managed {
				skillBadge(c, "市场安装", true)
			}
			if !boolean(skill["model_invocation"]) {
				skillBadge(c, "仅显式调用", false)
			}
			ui.Spacer(c)
			if a.current().RemoteID != "" && shadowed == "" {
				a.action(c, "使用").OnClick(func() { a.current().Draft += "\n使用技能 " + name + " "; a.closeSettings() })
			}
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
		if d := str(skill["description"]); d != "" {
			ui.Text(c, d).FontSize(12).LineHeight(1.55).TextColor(c.Theme().TextMuted).MaxLines(2).Tooltip(d)
		}
		if shadowed != "" {
			ui.Text(c, "同名技能已被更靠前的目录覆盖，这个不会被加载："+shadowed).FontSize(11.5).TextColor(c.Theme().TextMuted)
		}
		ui.Text(c, path).Font("monospace").FontSize(11).TextColor(c.Theme().TextMuted).Selectable()
	})
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
