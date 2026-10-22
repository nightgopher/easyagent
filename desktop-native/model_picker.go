package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

type nativeModelChoice struct {
	ID, Name, Provider string
	Window             int // known context window in tokens, 0 if unknown
}

// recentModelLimit is how many recently chosen models are listed first.
const recentModelLimit = 3

// detail is the second line of a choice: provider, ID when it differs from the
// name, and the context window when the host knows it.
func (m nativeModelChoice) detail() string {
	var parts []string
	if m.Provider != "" {
		parts = append(parts, m.Provider)
	}
	if m.ID != m.Name {
		parts = append(parts, m.ID)
	}
	if m.Window > 0 {
		parts = append(parts, formatWindow(m.Window)+" 上下文")
	}
	return strings.Join(parts, " · ")
}

func formatWindow(tokens int) string {
	if tokens >= 1000000 && tokens%1000000 == 0 {
		return fmt.Sprintf("%dM", tokens/1000000)
	}
	return fmt.Sprintf("%dK", (tokens+500)/1000)
}

// rememberModel moves a chosen model to the front of the recent list.
func (l *liveState) rememberModel(id string) {
	recent := []string{id}
	for _, m := range l.RecentModels {
		if m != id && len(recent) < recentModelLimit {
			recent = append(recent, m)
		}
	}
	l.RecentModels = recent
}

func (a *preview) modelChoices(query string) []nativeModelChoice {
	all := map[string]nativeModelChoice{}
	for _, raw := range list(object(a.Live.Data["models"])["models"]) {
		item := object(raw)
		id := str(item["id"])
		if id == "" {
			id = str(item["modelId"])
		}
		if id == "" {
			continue
		}
		name := str(item["name"])
		if name == "" {
			name = id
		}
		all[id] = nativeModelChoice{id, name, str(item["provider"]), int(number(item["context_window"]))}
	}
	var out []nativeModelChoice
	query = strings.ToLower(query)
	// Recently chosen models come first, then the host's order.
	order := []string{}
	seen := map[string]bool{}
	for _, id := range append(append([]string(nil), a.Live.RecentModels...), a.Live.Models...) {
		// A recent model the host no longer offers is skipped.
		if !seen[id] && slices.Contains(a.Live.Models, id) {
			seen[id] = true
			order = append(order, id)
		}
	}
	for _, id := range order {
		choice := all[id]
		if choice.ID == "" {
			choice = nativeModelChoice{ID: id, Name: id}
		}
		if strings.Contains(strings.ToLower(choice.ID+" "+choice.Name+" "+choice.Provider), query) {
			out = append(out, choice)
		}
	}
	return out
}
func (a *preview) liveModelPicker(c *ui.Context, anchor ui.Element) {
	l, s := a.Live, a.current()
	popup := a.menuPanel(c, anchor, &a.ModelMenu, func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			menuCaption(c, "选择模型 · ↑↓ 切换 · Enter 确认")
			ui.Spacer(c)
			// Refreshing is a small hint beside the caption, not a list row.
			if l.Loading["model-list"] {
				ui.Text(c, "刷新中…").Label("正在刷新模型目录").FontSize(10).TextColor(c.Theme().TextMuted).Padding(0, 8)
			}
		})
		input := ui.TextInput(c, &a.ModelSearch).Label("搜索模型").Placeholder("搜索模型…").AutoFocus().Padding(7).Background(c.Theme().Surface).Border(1, c.Theme().Border)
		if input.Changed() {
			l.ModelIndex = 0
			l.ModelJump = true
		}
		composing := input.Composing()
		input.OnTextKey(func(k ui.TextKey) bool {
			if composing || a.current() != s {
				return false
			}
			a.ModelSearch = k.Text()
			choices := a.modelChoices(a.ModelSearch)
			if len(choices) > 0 {
				if k.Key == ui.KeyUp || k.Key == ui.KeyDown {
					step := 1
					if k.Key == ui.KeyUp {
						step = -1
					}
					l.ModelIndex = (l.ModelIndex + step + len(choices)) % len(choices)
					l.ModelJump = true
					return true
				}
				if k.Key == ui.KeyEnter {
					a.switchLiveModel(choices[(l.ModelIndex%len(choices)+len(choices))%len(choices)].ID)
					return true
				}
			}
			return false
		})
		if err := l.ResourceErrors["model-list"]; err != "" {
			a.note(c, err)
			a.action(c, "重试模型目录").OnClick(a.refreshModels)
		}
		choices := a.modelChoices(a.ModelSearch)
		if len(choices) == 0 && !l.Loading["model-list"] {
			a.note(c, "没有匹配的模型")
		}
		if l.ModelJump {
			l.ModelList.ScrollIntoView(l.ModelIndex)
			l.ModelJump = false
		}
		ui.List(c, &l.ModelList, len(choices)).Label("模型列表").MaxHeight(260).Shrink(0).
			ItemKey(func(i int) any { return choices[i].ID }).Rows(func(row ui.ListRow) {
			c, i := row.Context, row.Index
			choice := choices[i]
			button := ui.ButtonBase(c.Key(choice.ID)).TextColor(c.Theme().Text).FillWidth().MinWidth(0).Padding(8, 10).Gap(8).Radius(5).Selected(s.Model == choice.ID).Label("选择模型：" + choice.ID).Disabled(l.Loading["model/"+s.RemoteID]).OnClick(func() { a.switchLiveModel(choice.ID) })
			if s.Model == choice.ID {
				button.Background(c.Theme().Accent.Alpha(.12)).TextColor(c.Theme().Accent)
			}
			if i == l.ModelIndex {
				button.Background(c.Theme().Text.Alpha(.08))
			}
			button.Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, choice.Name).FontSize(12).SingleLine().MinWidth(0)
					if detail := choice.detail(); detail != "" {
						ui.Text(c, detail).FontSize(10).TextColor(c.Theme().TextMuted).SingleLine().MinWidth(0)
					}
				})
				if s.Model == choice.ID {
					icon(c, "check")
				}
			})
		})
		if str(object(l.Data["models"])["source"]) == "configured" {
			a.note(c, "仅显示已配置模型，连接尚未验证")
		}
		if notice := str(object(l.Data["models"])["notice"]); notice != "" {
			a.note(c, notice)
		}
		a.action(c, "模型连接设置").AlignSelf(ui.Start).OnClick(func() { a.ModelMenu = false; a.openSettings("models") })
	})
	if popup.Valid() {
		popup.AttachTo(anchor, ui.AnchorTopLeft, ui.AnchorBottomLeft).Width(328).Margin(0, 0, 8, 0)
	}
}
