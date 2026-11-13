package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// Expert market: remote expert/team templates installed into business agents
// and teams. The catalog comes from the same marketplace as the skill store;
// templates are displayed with zh copy and installed synchronously via
// POST /experts/market/install.

func (a *preview) expertTemplates() []nativeExpertTemplate {
	return businessDecode[[]nativeExpertTemplate](object(a.Live.Data["expert-market"])["items"])
}

// expertInstalled maps template ids that were installed from this host.
func (a *preview) expertInstalled() map[string]bool {
	installed := map[string]bool{}
	for _, r := range businessDecode[[]nativeExpertInstallRecord](object(a.Live.Data["expert-market"])["installed"]) {
		installed[r.TemplateID] = true
	}
	return installed
}

func (a *preview) loadExpertMarket() {
	a.marketRead("expert-market", "/experts/market/templates", nil)
}

func (a *preview) installExpertTemplate(t nativeExpertTemplate) {
	s := &a.Settings.Business
	a.operation("expert-install-"+t.ID, "POST", "/experts/market/install", map[string]any{"id": t.ID, "kind": t.Kind}, func(v any) {
		a.Settings.message("已安装「" + t.Name.Zh + "」；权限与说明将在下次执行时生效。")
		s.ExpertHint = ""
		a.refreshBusiness()
		a.loadExpertMarket()
		if missing := businessDecode[[]string](object(v)["missing_skills"]); len(missing) > 0 {
			s.ExpertHint = "缺少技能：" + strings.Join(missing, "、") + "。绑定已保留，可在「技能」页的市场安装后自动生效。"
		}
	})
}

var expertCategories = []businessChoice{
	{"technology", "技术"},
	{"office", "办公"},
	{"creative", "创意"},
	{"life", "生活"},
}

// expertIcon maps a catalog icon id to this app's icon set; unknown or
// missing glyphs fall back to sparkle.
func expertIcon(name string) string {
	switch name {
	case "file-text":
		return "file"
	case "flask", "shapes", "":
		return "sparkle"
	default:
		if icons[name] != nil {
			return name
		}
		return "sparkle"
	}
}

// expertIconTile renders the card's leading icon inside a rounded tile.
func (a *preview) expertIconTile(c *ui.Context, iconID string) {
	ui.Column(c.Key("icon-" + iconID)).Size(36, 36).Center().Radius(8).Background(c.Theme().Accent.Alpha(.12)).Children(func() {
		if svg := icons[expertIcon(iconID)]; svg != nil {
			ui.Icon(c, svg).FontSize(19).TextColor(c.Theme().Accent)
		}
	})
}

func (a *preview) expertCategoryLabel(category string) string {
	for _, choice := range expertCategories {
		if choice.ID == category {
			return choice.Label
		}
	}
	return category
}

// expertMatchesQuery filters by the market search box: name, description and
// tags in both languages, case-insensitive.
func expertMatchesQuery(t nativeExpertTemplate, q string) bool {
	if q == "" {
		return true
	}
	haystack := strings.ToLower(t.Name.Zh + " " + t.Name.En + " " + t.Description.Zh + " " + t.Description.En)
	for _, tag := range t.Tags {
		haystack += " " + strings.ToLower(tag.Zh + " " + tag.En)
	}
	if t.Kind == "team" {
		haystack += " " + strings.ToLower(t.Roles.Zh + " " + t.Roles.En)
	}
	return strings.Contains(haystack, q)
}

func (a *preview) expertMarketView(c *ui.Context) {
	s := &a.Settings.Business
	l := a.Live
	ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.TextInput(c, &s.ExpertQuery).Label("搜索专家市场").Placeholder("搜索名称、描述或标签…").Grow(1).Basis(200).MinWidth(0).OnSubmit(func() {})
		ui.Spacer(c)
		a.action(c, "刷新").Disabled(l.Loading["expert-market"]).OnClick(a.loadExpertMarket)
	})
	ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
		for _, f := range []businessChoice{{"", "全部"}, {"expert", "专家"}, {"team", "专家团"}} {
			selected := s.ExpertKind == f.ID
			btn := flatButton(c, "", a.tr(f.Label)).Padding(5, 10)
			if selected {
				btn.Background(c.Theme().Accent.Alpha(.12)).TextColor(c.Theme().Accent)
			}
			btn.OnClick(func() { s.ExpertKind = f.ID })
		}
		for _, choice := range expertCategories {
			selected := s.ExpertCategory == choice.ID
			btn := flatButton(c, "", a.tr(choice.Label)).Padding(5, 10)
			if selected {
				btn.Background(c.Theme().Accent.Alpha(.12)).TextColor(c.Theme().Accent)
			}
			btn.OnClick(func() { s.ExpertCategory = choice.ID })
		}
	})

	if m := a.marketState(); m.Error != "" && l.Data["expert-market"] == nil {
		ui.Column(c).Padding(16).Gap(10).Children(func() {
			ui.Text(c, m.Error).FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
			a.action(c, "重试").AlignSelf(ui.Start).OnClick(func() { m.Error = ""; a.loadExpertMarket() })
		})
		return
	}
	if l.Loading["expert-market"] && l.Data["expert-market"] == nil {
		a.note(c, "正在读取专家目录…")
		return
	}
	if s.ExpertHint != "" {
		a.note(c, s.ExpertHint)
	}

	templates := a.expertTemplates()
	installed := a.expertInstalled()
	byID := map[string]nativeExpertTemplate{}
	for _, t := range templates {
		byID[t.ID] = t
	}
	query := strings.ToLower(strings.TrimSpace(s.ExpertQuery))
	count := 0
	for _, t := range templates {
		if s.ExpertKind != "" && t.Kind != s.ExpertKind {
			continue
		}
		if s.ExpertCategory != "" && t.Category != s.ExpertCategory {
			continue
		}
		if !expertMatchesQuery(t, query) {
			continue
		}
		count++
		if s.ExpertDetail == t.ID {
			a.expertMarketDetail(c, t, byID, installed[t.ID])
			continue
		}
		ui.Column(c.Key(t.ID)).Label("专家模板："+t.Name.Zh).Padding(14, 0).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
			ui.Row(c).Wrap().Gap(10).AlignItems(ui.Center).Children(func() {
				a.expertIconTile(c, t.Icon)
				ui.Text(c, t.Name.Zh).FontSize(14).FontWeight(600).Grow(1).MinWidth(0)
				if t.Kind == "team" {
					a.businessBadge(c, "专家团")
					a.businessBadge(c, fmt.Sprintf("负责人 + %d 名成员", len(t.Members)))
				} else {
					a.businessBadge(c, "专家")
				}
				a.businessBadge(c, a.expertCategoryLabel(t.Category))
				if installed[t.ID] {
					a.businessBadge(c, "已安装过")
				}
				a.action(c, "详情").Label("查看模板：" + t.Name.Zh).OnClick(func() { s.ExpertDetail = t.ID })
				a.primary(c, "安装").Label("安装模板：" + t.Name.Zh).Disabled(!l.Connected || l.Loading["expert-install-"+t.ID]).OnClick(func() { a.installExpertTemplate(t) })
			})
			ui.Text(c, t.Description.Zh).FontSize(12).TextColor(c.Theme().TextMuted).MaxLines(2).LineHeight(1.6)
			if len(t.Tags) > 0 {
				ui.Row(c).Wrap().Gap(6).Children(func() {
					for _, tag := range t.Tags {
						a.businessBadge(c, tag.Zh)
					}
				})
			}
		})
	}
	if count == 0 {
		a.note(c, "没有匹配的模板，请调整筛选或搜索词。")
	}
}

// expertMarketDetail expands one template: role prompt preview, team roster,
// and the install action.
func (a *preview) expertMarketDetail(c *ui.Context, t nativeExpertTemplate, byID map[string]nativeExpertTemplate, installed bool) {
	s := &a.Settings.Business
	l := a.Live
	ui.Column(c.Key("detail-" + t.ID)).Padding(14).Gap(8).Radius(8).Background(c.Theme().Surface).Children(func() {
		ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			a.expertIconTile(c, t.Icon)
			ui.Text(c, t.Name.Zh).FontSize(15).FontWeight(600).Grow(1).MinWidth(0)
			if t.Kind == "team" {
				a.businessBadge(c, "专家团")
			} else {
				a.businessBadge(c, "专家")
			}
			a.action(c, "返回列表").AlignSelf(ui.Center).OnClick(func() { s.ExpertDetail = "" })
			a.primary(c, "安装").Label("安装模板：" + t.Name.Zh).Disabled(!l.Connected || l.Loading["expert-install-"+t.ID]).OnClick(func() { a.installExpertTemplate(t) })
		})
		if installed {
			a.note(c, "此前已从市场安装过该模板；再次安装会创建新的 Agent，不会覆盖已有的。")
		}
		ui.Text(c, t.Description.Zh).FontSize(12).TextColor(c.Theme().TextMuted).LineHeight(1.6)
		if t.Kind == "team" {
			roster := []string{}
			if lead, ok := byID[t.Leader]; ok {
				roster = append(roster, lead.Name.Zh+"（负责人）")
			}
			for _, id := range t.Members {
				if member, ok := byID[id]; ok {
					roster = append(roster, member.Name.Zh)
				}
			}
			a.note(c, "团队分工："+t.Roles.Zh)
			a.note(c, "安装时会一起创建成员："+strings.Join(roster, "、"))
		}
		if len(t.Skills) > 0 {
			a.note(c, "绑定技能："+strings.Join(t.Skills, "、"))
		}
		a.heading(c, "角色说明预览")
		ui.Scroll(c).MaxHeight(300).Children(func() {
			ui.Text(c, t.Instructions.Zh).FontSize(12).LineHeight(1.7).Selectable()
		})
	})
}
