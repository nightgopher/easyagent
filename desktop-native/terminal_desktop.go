//go:build !android && !ios

package main

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"github.com/gorilla/websocket"
)

// gpuTerminalView wraps the plugins/terminal GPU-rendered view.
type gpuTerminalView struct {
	term *terminal.Terminal
}

func (v gpuTerminalView) done() <-chan struct{} { return v.term.Done() }
func (v gpuTerminalView) send(b []byte)         { v.term.Send(b) }
func (v gpuTerminalView) close()                { _ = v.term.Close() }
func (v gpuTerminalView) render(c *ui.Context) ui.Element {
	return terminal.View(c, v.term).Grow(1).FillWidth()
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
		var view terminalView
		if err == nil {
			conn.SetReadLimit(128 << 10)
			term, termErr := terminal.New(terminal.Options{Conn: &remoteTerminal{conn: conn}, Scrollback: 4 << 20})
			if termErr != nil {
				err = termErr
				_ = conn.Close()
			} else {
				view = gpuTerminalView{term: term}
			}
		}
		a.postOwned(epoch, func() {
			// The tab may have been closed, or the host changed, while dialing.
			if !w.hasTerminal(tab) || tab.Host != l.Config.ActiveHost {
				if view != nil {
					view.close()
				}
				return
			}
			tab.Starting = false
			if err != nil {
				tab.Error = "终端连接失败：" + err.Error()
				tab.Pending = ""
				return
			}
			tab.view = view
			if tab.Pending != "" {
				view.send([]byte(tab.Pending))
				tab.Pending = ""
			}
		}, func() {
			if view != nil {
				view.close()
			}
		})
	}()
	return tab
}
