package main

import (
	"fmt"
	"path/filepath"

	"github.com/egoist/mygo/ui"
)

// maxTerminalTabs bounds the shells one window keeps open on a host.
const maxTerminalTabs = 8

// terminalView 抽象一个已连接的终端：桌面端由 plugins/terminal（GPU 渲染）实现，
// 手机端暂不支持（该平台没有 libghostty-vt 原生库），构造接口永远返回失败。
type terminalView interface {
	done() <-chan struct{}
	send([]byte)
	close()
	render(c *ui.Context) ui.Element
}

// terminalTab is one shell on the active host. Tabs outlive session switches;
// only closing the tab, disconnecting or changing host ends the shell.
type terminalTab struct {
	ID                       int
	Host, Session, Workspace string
	view                     terminalView
	Starting                 bool
	Pending, Error           string
}

func (t *terminalTab) title() string {
	name := filepath.Base(t.Workspace)
	if name == "." || name == "/" || name == "" {
		name = "终端"
	}
	return fmt.Sprintf("%s · %d", name, t.ID)
}

func (t *terminalTab) close() {
	if t.view != nil {
		t.view.close()
		t.view = nil
	}
}

// activeTerminal returns the selected tab, or nil when none is open.
func (w *nativeWorkbench) activeTerminal() *terminalTab {
	for _, t := range w.Terminals {
		if t.ID == w.ActiveTerminal {
			return t
		}
	}
	return nil
}

// closeTerminal ends one shell and selects a neighbour.
func (w *nativeWorkbench) closeTerminal(tab *terminalTab) {
	for i, t := range w.Terminals {
		if t != tab {
			continue
		}
		t.close()
		w.Terminals = append(w.Terminals[:i], w.Terminals[i+1:]...)
		if w.ActiveTerminal == tab.ID {
			w.ActiveTerminal = 0
			if len(w.Terminals) > 0 {
				w.ActiveTerminal = w.Terminals[max(0, i-1)].ID
			}
		}
		break
	}
	if len(w.Terminals) == 0 {
		w.TerminalOpen = false
	}
}

// closeOtherHosts ends shells that belong to a host other than host.
func (w *nativeWorkbench) closeOtherHosts(host string) bool {
	changed := false
	for _, t := range append([]*terminalTab(nil), w.Terminals...) {
		if t.Host != host {
			w.closeTerminal(t)
			changed = true
		}
	}
	return changed
}

func (w *nativeWorkbench) hasTerminal(tab *terminalTab) bool {
	for _, t := range w.Terminals {
		if t == tab {
			return true
		}
	}
	return false
}

// sendToTerminal types text into the active shell, opening one if needed.
func (a *preview) sendToTerminal(text string) {
	w := &a.Live.Workbench
	tab := w.activeTerminal()
	if tab == nil {
		if tab = a.newTerminal(); tab == nil {
			return
		}
	}
	w.TerminalOpen = true
	if tab.view != nil {
		tab.view.send([]byte(text))
	} else if tab.Starting {
		tab.Pending = text
	}
}
