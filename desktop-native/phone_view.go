package main

import (
	"github.com/egoist/mygo/ui"
	"strings"
)

// The phone uses one full-width destination at a time, instead of desktop panes.
func (a *preview) phoneChrome(c *ui.Context) {
	ui.Row(c).Height(52).Padding(0, 12).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
		if a.Settings.Open {
			iconButton(c, "arrow-left", "返回对话").OnClick(a.closeSettings)
			ui.Text(c, "设置").FontSize(17).FontWeight(600)
		} else {
			iconButton(c, "panel", "会话列表").OnClick(func() { a.Sidebar = !a.Sidebar; a.Workbench = false })
			label := "EasyAgent"
			if a.Sidebar {
				label = "对话"
			}
			ui.Text(c, label).FontSize(17).FontWeight(600)
		}
		ui.Spacer(c)
		if !a.Settings.Open {
			ui.Text(c, a.connectionLabel()).FontSize(11).TextColor(c.Theme().TextMuted)
			iconButton(c, "settings", "设置").OnClick(func() { a.openSettings("connections") })
		}
	})
}

func (a *preview) phoneSettingsMenu(c *ui.Context) {
	s := &a.Settings
	ui.Scroll(c).Grow(1).MinHeight(0).Padding(16).Children(func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, "设置").FontSize(22).FontWeight(600)
			for group, title := range []string{"基础设置", "连接与能力", "业务能力"} {
				ui.Text(c, title).Padding(12, 4, 4, 4).FontSize(12).TextColor(c.Theme().TextMuted)
				for _, page := range settingsPages {
					if page.Group != group {
						continue
					}
					p := page
					ui.ButtonBase(c.Key(p.ID)).Label(a.tr(p.Label)).FillWidth().MinHeight(48).Padding(10, 12).Gap(10).Justify(ui.Start).Radius(8).Selected(p.ID == s.Tab).OnClick(func() { a.openSettings(p.ID) }).Children(func() {
						icon(c, p.Icon)
						a.text(c, p.Label)
					})
				}
			}
		})
	})
}

// phoneSessions is the phone's home screen; opening a conversation replaces it.
func (a *preview) phoneSessions(c *ui.Context) {
	l := a.Live
	ui.Column(c).Grow(1).MinWidth(0).Padding(16).Gap(12).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			host := flatButton(c, "laptop", a.Settings.host().Name).Label("切换运行主机").Grow(1).MinWidth(0).Justify(ui.Start).OnClick(func() { a.toggleMenu(&a.HostMenu) })
			a.menuPanel(c, host, &a.HostMenu, func() {
				menuCaption(c, "运行主机")
				for i, h := range l.Config.Hosts {
					index := i
					menuOption(c.Key(h.ID), "laptop", h.Name, h.ID == l.Config.ActiveHost).OnClick(func() { a.HostMenu = false; a.selectLiveHost(index) })
				}
				menuOption(c, "settings", "管理运行主机", false).OnClick(func() { a.HostMenu = false; a.openSettings("connections") })
			})
			iconButton(c, "refresh", "刷新会话").Disabled(!l.Connected).OnClick(func() { a.refreshLiveSessions("") })
		})
		ui.Row(c).Gap(8).Children(func() {
			flatButton(c, "user", "管家").Grow(1).Height(44).Disabled(!l.Connected || l.Loading["steward"]).OnClick(a.openSteward)
			flatButton(c, "activity", "任务跟进").Grow(1).Height(44).OnClick(func() { a.openWork(); a.Sidebar = false })
		})
		search := ui.TextInputBase(c, &a.Search).Label("搜索会话").Placeholder("搜索会话内容…").FillWidth().Height(44).Padding(8, 12).Background(searchSurface(c)).Border(1, c.Theme().Border)
		l.SessionSearch.Composing = search.Composing()
		if search.Changed() && !search.Composing() {
			a.queueSessionSearch()
		}
		if search.Submitted() {
			a.queueSessionSearch()
			a.searchSessionPage(0)
		}
		if strings.TrimSpace(a.Search) != "" {
			a.sessionSearchControls(c)
		} else {
			ui.Row(c).Gap(10).Children(func() {
				flatButton(c, "", "最近").Selected(!a.Archived).OnClick(func() { a.Archived = false })
				flatButton(c, "", "已归档").Selected(a.Archived).OnClick(func() { a.Archived = true })
			})
		}
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			if strings.TrimSpace(a.Search) != "" {
				a.sessionSearchResults(c)
				return
			}
			count := 0
			for _, s := range a.Sessions {
				if s.Archived == a.Archived {
					count++
					a.sessionRow(c, s)
				}
			}
			if count == 0 {
				a.note(c, "还没有会话")
			}
		})
		flatButton(c, "plus", "新建会话").FillWidth().Height(48).Background(c.Theme().Accent).TextColor(c.Theme().AccentText).Disabled(!l.Connected || l.Loading["create-session"]).OnClick(a.newSession)
	})
}
