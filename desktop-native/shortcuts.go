package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// nativeShortcut is the single source for the global key dispatch in View and
// the shortcut list on the general settings page.
type nativeShortcut struct {
	ID          string
	Label, Keys string
	DefaultKeys string
	match       func(c *ui.Context, a *preview) bool
	run         func(a *preview)
}

var nativeShortcuts = []nativeShortcut{
	{ID: "send_message", Label: "发送消息", Keys: "Enter", DefaultKeys: "Enter"},
	{ID: "new_line", Label: "输入换行", Keys: "Shift + Enter", DefaultKeys: "Shift + Enter"},
	{ID: "command_menu", Label: "打开命令菜单", Keys: "/", DefaultKeys: "/"},
	{ID: "new_session", Label: "新会话", Keys: "⌘ N", DefaultKeys: "⌘ N"},
	{ID: "files", Label: "文件工作台", Keys: "⌘ P", DefaultKeys: "⌘ P"},
	{ID: "terminal", Label: "终端", Keys: "⌘ `", DefaultKeys: "⌘ `"},
	{ID: "review", Label: "代码审查", Keys: "⌘ Shift G", DefaultKeys: "⌘ Shift G"},
	{ID: "settings", Label: "设置", Keys: "⌘ ,", DefaultKeys: "⌘ ,"},
}

func init() {
	for i := range nativeShortcuts {
		switch nativeShortcuts[i].ID {
		case "new_session":
			nativeShortcuts[i].match = func(c *ui.Context, a *preview) bool {
				return a.matchShortcut("new_session", c) && !a.Settings.Open
			}
			nativeShortcuts[i].run = func(a *preview) { a.newSession() }
		case "files":
			nativeShortcuts[i].match = func(c *ui.Context, a *preview) bool {
				return a.matchShortcut("files", c) && !a.Settings.Open
			}
			nativeShortcuts[i].run = func(a *preview) {
				if a.Live != nil && (!a.Workbench || a.Live.Workbench.Tab != "files") {
					a.openResource("files")
				} else {
					a.Workbench = !a.Workbench
				}
			}
		case "terminal":
			nativeShortcuts[i].match = func(c *ui.Context, a *preview) bool {
				return a.Live != nil && !a.Settings.Open && a.matchShortcut("terminal", c)
			}
			nativeShortcuts[i].run = func(a *preview) { a.toggleTerminal() }
		case "review":
			nativeShortcuts[i].match = func(c *ui.Context, a *preview) bool {
				return a.Live != nil && a.matchShortcut("review", c)
			}
			nativeShortcuts[i].run = func(a *preview) { a.openResource("review") }
		case "settings":
			nativeShortcuts[i].match = func(c *ui.Context, a *preview) bool {
				return a.matchShortcut("settings", c)
			}
			nativeShortcuts[i].run = func(a *preview) { a.openSettings("general") }
		}
	}
}

func parseKeyName(name string) ui.Key {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch name {
	case "A":
		return ui.KeyA
	case "B":
		return ui.KeyB
	case "C":
		return ui.KeyC
	case "D":
		return ui.KeyD
	case "E":
		return ui.KeyE
	case "F":
		return ui.KeyF
	case "G":
		return ui.KeyG
	case "H":
		return ui.KeyH
	case "I":
		return ui.KeyI
	case "J":
		return ui.KeyJ
	case "K":
		return ui.KeyK
	case "L":
		return ui.KeyL
	case "M":
		return ui.KeyM
	case "N":
		return ui.KeyN
	case "O":
		return ui.KeyO
	case "P":
		return ui.KeyP
	case "Q":
		return ui.KeyQ
	case "R":
		return ui.KeyR
	case "S":
		return ui.KeyS
	case "T":
		return ui.KeyT
	case "U":
		return ui.KeyU
	case "V":
		return ui.KeyV
	case "W":
		return ui.KeyW
	case "X":
		return ui.KeyX
	case "Y":
		return ui.KeyY
	case "Z":
		return ui.KeyZ
	case "0":
		return ui.Key0
	case "1":
		return ui.Key1
	case "2":
		return ui.Key2
	case "3":
		return ui.Key3
	case "4":
		return ui.Key4
	case "5":
		return ui.Key5
	case "6":
		return ui.Key6
	case "7":
		return ui.Key7
	case "8":
		return ui.Key8
	case "9":
		return ui.Key9
	case "`", "~":
		return ui.KeyBackquote
	case ",", "<":
		return ui.KeyComma
	case ".", ">":
		return ui.KeyPeriod
	case "/", "?":
		return ui.KeySlash
	case ";", ":":
		return ui.KeySemicolon
	case "-", "_":
		return ui.KeyMinus
	case "=", "+":
		return ui.KeyEqual
	case "ENTER", "RETURN":
		return ui.KeyEnter
	case "TAB":
		return ui.KeyTab
	case "SPACE":
		return ui.KeySpace
	default:
		return 0
	}
}

func parseShortcut(keys string) (ui.Modifiers, ui.Key, bool) {
	keys = strings.TrimSpace(keys)
	var mods ui.Modifiers
	parts := strings.Split(keys, " ")
	var keyPart string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "+" {
			continue
		}
		switch p {
		case "⌘", "Cmd", "Command":
			mods |= ui.Cmd
		case "Shift":
			mods |= ui.Shift
		case "Alt", "Option", "⌥":
			mods |= ui.Alt
		case "Ctrl", "Control", "^":
			mods |= ui.Ctrl
		default:
			keyPart = p
		}
	}
	key := parseKeyName(keyPart)
	if key == 0 && keyPart != "" {
		return 0, 0, false
	}
	return mods, key, true
}

func (a *preview) matchShortcut(id string, c *ui.Context) bool {
	for i := range nativeShortcuts {
		s := &nativeShortcuts[i]
		if s.ID == id {
			mods, key, ok := parseShortcut(s.Keys)
			if ok {
				return c.Shortcut(mods, key)
			}
		}
	}
	return false
}

func (a *preview) shortcutLabel(label string) string {
	for i := range nativeShortcuts {
		if nativeShortcuts[i].Label == label {
			return nativeShortcuts[i].Keys
		}
	}
	return ""
}

func (a *preview) resetAllShortcuts() {
	for i := range nativeShortcuts {
		nativeShortcuts[i].Keys = nativeShortcuts[i].DefaultKeys
	}
	a.Settings.CapturingShortcutID = ""
	a.Settings.ShortcutConflict = ""
}

func (a *preview) resetSingleShortcut(id string) {
	for i := range nativeShortcuts {
		if nativeShortcuts[i].ID == id {
			nativeShortcuts[i].Keys = nativeShortcuts[i].DefaultKeys
			if a.Settings.Preferences.Shortcuts != nil {
				delete(a.Settings.Preferences.Shortcuts, id)
				a.Settings.savePreferences()
			}
			break
		}
	}
	a.Settings.CapturingShortcutID = ""
	a.Settings.ShortcutConflict = ""
}

func (a *preview) applyCustomShortcut(id, keys string) error {
	for i := range nativeShortcuts {
		other := &nativeShortcuts[i]
		if other.ID != id && other.Keys == keys {
			return fmt.Errorf("与 [%s] 快捷键冲突", other.Label)
		}
	}
	for i := range nativeShortcuts {
		if nativeShortcuts[i].ID == id {
			nativeShortcuts[i].Keys = keys
			if a.Settings.Preferences.Shortcuts == nil {
				a.Settings.Preferences.Shortcuts = make(map[string]string)
			}
			a.Settings.Preferences.Shortcuts[id] = keys
			a.Settings.savePreferences()
			break
		}
	}
	return nil
}

func detectPressedShortcut(c *ui.Context) (string, bool) {
	modifiersList := []struct {
		mods   ui.Modifiers
		prefix string
	}{
		{ui.Cmd | ui.Shift, "⌘ Shift "},
		{ui.Cmd | ui.Alt, "⌘ ⌥ "},
		{ui.Cmd, "⌘ "},
		{ui.Ctrl, "Ctrl "},
		{ui.Alt, "⌥ "},
	}
	type keyEntry struct {
		k ui.Key
		n string
	}
	keys := []keyEntry{
		{ui.KeyA, "A"}, {ui.KeyB, "B"}, {ui.KeyC, "C"}, {ui.KeyD, "D"},
		{ui.KeyE, "E"}, {ui.KeyF, "F"}, {ui.KeyG, "G"}, {ui.KeyH, "H"},
		{ui.KeyI, "I"}, {ui.KeyJ, "J"}, {ui.KeyK, "K"}, {ui.KeyL, "L"},
		{ui.KeyM, "M"}, {ui.KeyN, "N"}, {ui.KeyO, "O"}, {ui.KeyP, "P"},
		{ui.KeyQ, "Q"}, {ui.KeyR, "R"}, {ui.KeyS, "S"}, {ui.KeyT, "T"},
		{ui.KeyU, "U"}, {ui.KeyV, "V"}, {ui.KeyW, "W"}, {ui.KeyX, "X"},
		{ui.KeyY, "Y"}, {ui.KeyZ, "Z"},
		{ui.Key0, "0"}, {ui.Key1, "1"}, {ui.Key2, "2"}, {ui.Key3, "3"},
		{ui.Key4, "4"}, {ui.Key5, "5"}, {ui.Key6, "6"}, {ui.Key7, "7"},
		{ui.Key8, "8"}, {ui.Key9, "9"},
		{ui.KeyBackquote, "`"}, {ui.KeyComma, ","}, {ui.KeyPeriod, "."},
		{ui.KeySlash, "/"}, {ui.KeySemicolon, ";"}, {ui.KeyMinus, "-"}, {ui.KeyEqual, "="},
	}

	for _, m := range modifiersList {
		for _, ke := range keys {
			if c.Shortcut(m.mods, ke.k) {
				return m.prefix + ke.n, true
			}
		}
	}
	return "", false
}

// dispatchNativeShortcuts runs the global keyboard shortcuts before layout.
func (a *preview) dispatchNativeShortcuts(c *ui.Context) {
	if a.Settings.CapturingShortcutID != "" {
		if c.Shortcut(0, ui.KeyEscape) {
			a.Settings.CapturingShortcutID = ""
			a.Settings.ShortcutConflict = ""
			return
		}
		if newKeys, ok := detectPressedShortcut(c); ok {
			if err := a.applyCustomShortcut(a.Settings.CapturingShortcutID, newKeys); err != nil {
				a.Settings.ShortcutConflict = err.Error()
			} else {
				a.Settings.CapturingShortcutID = ""
				a.Settings.ShortcutConflict = ""
				c.Toast(a.tr("已更新快捷键"))
			}
			return
		}
	}
	for i := range nativeShortcuts {
		shortcut := &nativeShortcuts[i]
		if shortcut.match != nil && shortcut.match(c, a) {
			shortcut.run(a)
		}
	}
}
