package main

import "github.com/egoist/mygo/ui"

// nativeShortcuts is the single source for the global key dispatch in View and
// the read-only shortcut list on the general settings page; entries without a
// matcher are composer-level keys listed for reference only.
type nativeShortcut struct {
	Label, Keys string
	match       func(c *ui.Context, a *preview) bool
	run         func(a *preview)
}

var nativeShortcuts = []nativeShortcut{
	{Label: "发送消息", Keys: "Enter"},
	{Label: "输入换行", Keys: "Shift + Enter"},
	{Label: "打开命令菜单", Keys: "/"},
	{Label: "新会话", Keys: "⌘ N", match: func(c *ui.Context, a *preview) bool {
		return c.Shortcut(ui.Cmd, ui.KeyN) && !a.Settings.Open
	}, run: func(a *preview) { a.newSession() }},
	{Label: "文件工作台", Keys: "⌘ P", match: func(c *ui.Context, a *preview) bool {
		return c.Shortcut(ui.Cmd, ui.KeyP) && !a.Settings.Open
	}, run: func(a *preview) {
		if a.Live != nil && (!a.Workbench || a.Live.Workbench.Tab != "files") {
			a.openResource("files")
		} else {
			a.Workbench = !a.Workbench
		}
	}},
	{Label: "终端", Keys: "⌘ `", match: func(c *ui.Context, a *preview) bool {
		return a.Live != nil && !a.Settings.Open && c.Shortcut(ui.Cmd, ui.KeyBackquote)
	}, run: func(a *preview) { a.toggleTerminal() }},
	{Label: "代码审查", Keys: "⌘ Shift G", match: func(c *ui.Context, a *preview) bool {
		return a.Live != nil && c.Shortcut(ui.Cmd|ui.Shift, ui.KeyG)
	}, run: func(a *preview) { a.openResource("review") }},
	{Label: "设置", Keys: "⌘ ,", match: func(c *ui.Context, a *preview) bool {
		return c.Shortcut(ui.Cmd, ui.KeyComma)
	}, run: func(a *preview) { a.openSettings("general") }},
}

// dispatchNativeShortcuts runs the global keyboard shortcuts before layout.
func (a *preview) dispatchNativeShortcuts(c *ui.Context) {
	for i := range nativeShortcuts {
		shortcut := &nativeShortcuts[i]
		if shortcut.match != nil && shortcut.match(c, a) {
			shortcut.run(a)
		}
	}
}
