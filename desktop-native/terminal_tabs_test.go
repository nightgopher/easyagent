package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/gorilla/websocket"
)

// fakeTerminalHost accepts terminal websockets and counts open connections.
func fakeTerminalHost(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var open atomic.Int32
	upgrader := websocket.Upgrader{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/terminal" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		open.Add(1)
		defer open.Add(-1)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(s.Close)
	return s, &open
}

func terminalTabsPreview(t *testing.T) (*preview, *atomic.Int32) {
	srv, open := fakeTerminalHost(t)
	a := newConnectedPreview(t.TempDir())
	t.Cleanup(a.closeLive)
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.Live.Connected = true
	a.Sessions = []*session{{ID: 1, RemoteID: "A", Workspace: "/work/api"}, {ID: 2, RemoteID: "B", Workspace: "/work/web"}}
	a.Active = 1
	return a, open
}

// Several shells run side by side and survive switching sessions (#260).
func TestTerminalTabsCoexistAcrossSessions(t *testing.T) {
	a, open := terminalTabsPreview(t)
	w := &a.Live.Workbench
	a.toggleTerminal()
	a.newTerminal()
	waitNative(t, a, func() bool { return open.Load() == 2 && w.Terminals[0].Term != nil && w.Terminals[1].Term != nil })
	if w.ActiveTerminal != 2 || !w.TerminalOpen {
		t.Fatalf("new tab not selected: %+v", w)
	}

	a.Active = 2 // switch session
	a.liveTick()
	if len(w.Terminals) != 2 || w.Terminals[0].Term == nil {
		t.Fatal("switching sessions closed a shell")
	}

	a.toggleTerminal() // hide keeps shells
	a.toggleTerminal()
	if len(w.Terminals) != 2 || !w.TerminalOpen {
		t.Fatal("hiding the panel closed shells")
	}

	w.closeTerminal(w.Terminals[0])
	waitNative(t, a, func() bool { return open.Load() == 1 })
	if len(w.Terminals) != 1 || w.ActiveTerminal != 2 {
		t.Fatalf("closing a tab: %+v", w.Terminals)
	}

	a.Live.Config.ActiveHost = "elsewhere"
	a.liveTick()
	waitNative(t, a, func() bool { return open.Load() == 0 })
	if len(w.Terminals) != 0 || w.TerminalOpen {
		t.Fatal("shells of another host kept running")
	}
}

func TestTerminalTabLimit(t *testing.T) {
	a, _ := terminalTabsPreview(t)
	for i := 0; i < maxTerminalTabs; i++ {
		if a.newTerminal() == nil {
			t.Fatalf("tab %d refused", i)
		}
	}
	if a.newTerminal() != nil || a.Live.Error == "" {
		t.Fatal("tab limit not enforced")
	}
}

func TestTerminalTabBarSwitches(t *testing.T) {
	a, open := terminalTabsPreview(t)
	w := &a.Live.Workbench
	a.toggleTerminal()
	a.newTerminal()
	waitNative(t, a, func() bool { return open.Load() == 2 })
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "切换终端：api · 1")
	if w.ActiveTerminal != 1 {
		t.Fatal("tab click did not switch")
	}
	click(t, tt, "关闭终端：api · 1")
	if len(w.Terminals) != 1 || w.ActiveTerminal != 2 {
		t.Fatal("tab close button failed")
	}
}
