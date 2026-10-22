package main

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/gorilla/websocket"
)

// maxTerminalTabs bounds the shells one window keeps open on a host.
const maxTerminalTabs = 8

// terminalTab is one shell on the active host. Tabs outlive session switches;
// only closing the tab, disconnecting or changing host ends the shell.
type terminalTab struct {
	ID                       int
	Host, Session, Workspace string
	Term                     *terminal.Terminal
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
	if t.Term != nil {
		_ = t.Term.Close()
		t.Term = nil
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

// newTerminal opens another shell in the current session's workspace.
func (a *preview) newTerminal() *terminalTab {
	l := a.Live
	w := &l.Workbench
	s := a.current()
	if s.RemoteID == "" {
		l.Error = "请先创建会话，再打开终端"
		return nil
	}
	if l.Client == nil {
		l.Error = "请先连接运行主机"
		return nil
	}
	if len(w.Terminals) >= maxTerminalTabs {
		l.Error = fmt.Sprintf("最多同时打开 %d 个终端，请先关闭不用的终端", maxTerminalTabs)
		return nil
	}
	w.NextTerminalID++
	tab := &terminalTab{ID: w.NextTerminalID, Host: l.Config.ActiveHost, Session: s.RemoteID, Workspace: s.Workspace, Starting: true}
	w.Terminals = append(w.Terminals, tab)
	w.ActiveTerminal = tab.ID
	w.TerminalOpen = true
	epoch, ctx, b, sid := l.epoch, l.hostCtx, l.Client, s.RemoteID
	go func() {
		u, _ := url.Parse(b.URL + "/terminal" + query(map[string]string{"session_id": sid, "cols": "80", "rows": "24"}))
		if u.Scheme == "https" {
			u.Scheme = "wss"
		} else {
			u.Scheme = "ws"
		}
		h := http.Header{}
		h.Set("Authorization", "Bearer "+b.Token)
		conn, resp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), h)
		if err != nil && resp != nil {
			err = terminalHandshakeError(resp)
		}
		var term *terminal.Terminal
		if err == nil {
			conn.SetReadLimit(128 << 10)
			term, err = terminal.New(terminal.Options{Conn: &remoteTerminal{conn: conn}, Scrollback: 4 << 20})
			if err != nil {
				_ = conn.Close()
			}
		}
		a.postOwned(epoch, func() {
			// The tab may have been closed, or the host changed, while dialing.
			if !w.hasTerminal(tab) || tab.Host != l.Config.ActiveHost {
				if term != nil {
					_ = term.Close()
				}
				return
			}
			tab.Starting = false
			if err != nil {
				tab.Error = "终端连接失败：" + err.Error()
				tab.Pending = ""
				return
			}
			tab.Term = term
			if tab.Pending != "" {
				term.Send([]byte(tab.Pending))
				tab.Pending = ""
			}
		}, func() {
			if term != nil {
				_ = term.Close()
			}
		})
	}()
	return tab
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
	if tab.Term != nil {
		tab.Term.Send([]byte(text))
	} else if tab.Starting {
		tab.Pending = text
	}
}
