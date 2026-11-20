package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

type nativeHook struct {
	Event          string   `json:"event"`
	Tool           string   `json:"tool"`
	Command        string   `json:"command"`
	Args           []string `json:"args,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	Enabled        bool     `json:"enabled"`
}

type nativeHookDocument struct {
	Hooks []nativeHook `json:"hooks"`
}

var hookEvents = []struct{ label, value string }{
	{"工具执行前", "before_tool"}, {"工具执行后", "after_tool"},
	{"任务开始", "session_start"}, {"任务结束", "session_end"}, {"上下文压缩前", "pre_compact"},
}

func hookEventLabel(value string) string {
	for _, event := range hookEvents {
		if event.value == value {
			return event.label
		}
	}
	return value
}

func hookEventValue(label string) string {
	for _, event := range hookEvents {
		if event.label == label {
			return event.value
		}
	}
	return ""
}

func (a *preview) loadHookSettings(force bool) {
	l, s := a.Live, &a.Settings
	if l == nil || l.Client == nil {
		return
	}
	if !force && s.HookEpoch == l.epoch && s.HookRevision != "" {
		return
	}
	a.operation("hooks-load", "GET", "/settings/hooks", nil, func(value any) {
		m := object(value)
		data, err := json.Marshal(map[string]any{"hooks": m["hooks"]})
		var doc nativeHookDocument
		if err != nil || json.Unmarshal(data, &doc) != nil || doc.Hooks == nil || str(m["revision"]) == "" {
			s.Error = "无法读取 hooks 配置"
			return
		}
		s.HookDraft, s.HookSaved, s.HookRevision, s.HookEpoch = string(data), string(data), str(m["revision"]), l.epoch
	})
}

func (a *preview) saveHookSettings() {
	l, s := a.Live, &a.Settings
	if l == nil || s.HookEpoch != l.epoch || s.HookRevision == "" {
		return
	}
	var doc nativeHookDocument
	if json.Unmarshal([]byte(s.HookDraft), &doc) != nil || doc.Hooks == nil {
		s.Error = "Hooks 配置无效"
		return
	}
	draft := s.HookDraft
	a.operation("hooks-save", "PUT", "/settings/hooks", map[string]any{"hooks": doc.Hooks, "revision": s.HookRevision}, func(value any) {
		m := object(value)
		if str(m["revision"]) == "" {
			s.Error = "保存结果无效，请重新加载"
			return
		}
		s.HookRevision, s.HookSaved = str(m["revision"]), draft
		s.message("Hooks 已保存，下次对应事件生效")
	})
}

func (a *preview) hookItems() []nativeHook {
	var doc nativeHookDocument
	_ = json.Unmarshal([]byte(a.Settings.HookDraft), &doc)
	return doc.Hooks
}

func (a *preview) setHookItems(items []nativeHook) {
	if items == nil {
		items = []nativeHook{}
	}
	data, _ := json.Marshal(nativeHookDocument{Hooks: items})
	a.Settings.HookDraft = string(data)
}

func (a *preview) editHook(index int) {
	s := &a.Settings
	h := nativeHook{Event: "before_tool", Tool: "*", TimeoutSeconds: 10, Enabled: true}
	items := a.hookItems()
	if index >= 0 && index < len(items) {
		h = items[index]
	} else {
		index = -1
	}
	s.HookEditIndex, s.HookFormOpen = index, true
	s.HookEvent = hookEventLabel(h.Event)
	s.HookTool, s.HookCommand, s.HookArgs, s.HookEnabled = h.Tool, h.Command, strings.Join(h.Args, "\n"), h.Enabled
	if h.TimeoutSeconds == 0 {
		h.TimeoutSeconds = 10
	}
	s.HookTimeout = strconv.Itoa(h.TimeoutSeconds)
}

func (a *preview) applyHookForm() {
	s := &a.Settings
	timeout, err := strconv.Atoi(strings.TrimSpace(s.HookTimeout))
	event := hookEventValue(s.HookEvent)
	tool := strings.TrimSpace(s.HookTool)
	if event != "before_tool" && event != "after_tool" && tool == "" {
		tool = "*"
	}
	if err != nil || timeout < 1 || timeout > 60 || tool == "" || strings.TrimSpace(s.HookCommand) == "" || event == "" {
		s.Error = "请填写工具名、命令和 1–60 秒的超时"
		return
	}
	var args []string
	if s.HookArgs != "" {
		args = strings.Split(strings.ReplaceAll(s.HookArgs, "\r\n", "\n"), "\n")
	}
	h := nativeHook{Event: event, Tool: tool, Command: strings.TrimSpace(s.HookCommand), Args: args, TimeoutSeconds: timeout, Enabled: s.HookEnabled}
	items := a.hookItems()
	if s.HookEditIndex < 0 {
		items = append(items, h)
	} else if s.HookEditIndex < len(items) {
		items[s.HookEditIndex] = h
	} else {
		s.Error = "Hook 已变化，请重新加载"
		return
	}
	a.setHookItems(items)
	s.HookFormOpen = false
	a.saveHookSettings()
}

func (a *preview) liveHookSettings(c *ui.Context) {
	l, s := a.Live, &a.Settings
	a.note(c, "Hooks 以当前主机账号运行，不经过工具审批，可读取工具参数；请只配置你信任的程序。")
	for _, key := range []string{"hooks-load", "hooks-save"} {
		if message := l.ResourceErrors[key]; message != "" {
			ui.Text(c, message).FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
		}
	}
	if l.Client == nil {
		a.note(c, "请先连接运行主机。")
		return
	}
	if l.Loading["hooks-load"] {
		a.note(c, "正在加载 hooks…")
		return
	}
	if s.HookEpoch != l.epoch || s.HookRevision == "" {
		a.action(c, "加载 hooks").OnClick(func() { a.loadHookSettings(true) })
		return
	}
	busy := l.Loading["hooks-save"] || l.Loading["hooks-load"]
	items := a.hookItems()
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		a.heading(c, fmt.Sprintf("已配置 Hooks · %d", len(items)))
		ui.Spacer(c)
		a.action(c, "新建 Hook").Disabled(busy || s.HookFormOpen).OnClick(func() { a.editHook(-1) })
	})
	if len(items) == 0 && !s.HookFormOpen {
		a.note(c, "还没有配置 Hook。")
	}
	for i, hook := range items {
		i, hook := i, hook
		ui.Column(c.Key(fmt.Sprintf("hook-%d", i))).Gap(6).Padding(12).Border(1, c.Theme().Border).Radius(8).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				label := hookEventLabel(hook.Event)
				if hook.Event == "before_tool" || hook.Event == "after_tool" {
					label += " · " + hook.Tool
				}
				ui.Text(c, label).Grow(1).MinWidth(0)
				enabled := hook.Enabled
				if ui.Switch(c, &enabled).Label(fmt.Sprintf("启用 Hook %d", i+1)).Disabled(busy || s.HookFormOpen).Changed() {
					updated := a.hookItems()
					updated[i].Enabled = enabled
					a.setHookItems(updated)
					a.saveHookSettings()
				}
				a.action(c, "编辑").Disabled(busy || s.HookFormOpen).OnClick(func() { a.editHook(i) })
				a.danger(c, "删除").Disabled(busy || s.HookFormOpen).OnClick(func() {
					a.requestConfirmation("删除 Hook？", hook.Command, func() {
						updated := a.hookItems()
						if i >= len(updated) {
							return
						}
						updated = append(updated[:i], updated[i+1:]...)
						a.setHookItems(updated)
						a.saveHookSettings()
					})
				})
			})
			ui.Text(c, hook.Command).Font("monospace").FontSize(12).TextColor(c.Theme().TextMuted)
		})
	}
	if s.HookFormOpen {
		if s.HookEditIndex < 0 {
			a.heading(c, "新建 Hook")
		} else {
			a.heading(c, "编辑 Hook")
		}
		a.dropdown(c, "触发事件", &s.HookEvent, []string{"工具执行前", "工具执行后", "任务开始", "任务结束", "上下文压缩前"})
		if s.HookEvent == "工具执行前" || s.HookEvent == "工具执行后" {
			a.input(c, "匹配工具", &s.HookTool, "例如 write，* 匹配全部", false)
		}
		a.input(c, "可执行文件（绝对路径）", &s.HookCommand, "例如 /usr/local/bin/check", false)
		a.multiline(c, "命令参数（每行一个）", &s.HookArgs, 2, 5)
		a.input(c, "超时（秒）", &s.HookTimeout, "10", false)
		ui.Checkbox(c, &s.HookEnabled, "启用此 Hook")
		a.note(c, "标准输入为 JSON；工具执行前退出失败或返回 deny 会阻止工具，其余事件失败仅记录日志。命令不自动经过 shell。")
		ui.Row(c).Gap(8).Children(func() {
			a.primary(c, "保存 Hook").Disabled(busy).OnClick(a.applyHookForm)
			a.action(c, "取消编辑").Disabled(busy).OnClick(func() { s.HookFormOpen = false })
		})
	}
	if s.HookDraft != s.HookSaved && !s.HookFormOpen {
		a.action(c, "重试保存 Hooks").Disabled(busy).OnClick(a.saveHookSettings)
	}
	a.action(c, "重新加载 Hooks").Disabled(busy || s.HookFormOpen).OnClick(func() {
		if s.HookDraft != s.HookSaved {
			a.requestConfirmation("放弃未保存的 Hooks 修改？", "重新加载会替换当前编辑内容。", func() { a.loadHookSettings(true) })
			return
		}
		a.loadHookSettings(true)
	})
}
