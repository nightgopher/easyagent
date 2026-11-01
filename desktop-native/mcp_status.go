package main

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const mcpStatusInterval = 2500 * time.Millisecond

type nativeMCPStatus struct {
	Workspace string
	NextPoll  time.Time
	Cancel    context.CancelFunc
}

func (a *preview) mcpStatusBusy() bool {
	for _, key := range []string{"mcp-action", "mcp-save", "mcp-tool", "mcp-trust", "mcp-login"} {
		if a.Live.Loading[key] {
			return true
		}
	}
	return false
}

func (a *preview) cancelMCPStatus() {
	l := a.Live
	l.Versions["mcp"]++
	if l.MCPStatus.Cancel != nil {
		l.MCPStatus.Cancel()
		l.MCPStatus.Cancel = nil
	}
	l.Loading["mcp"], l.Loading["mcp-poll"] = false, false
}

// MCP connects asynchronously. Keep the visible list in sync without opening
// another connection or making the controls flicker on each background read.
func (a *preview) tickMCPStatus() {
	l, s := a.Live, &a.Settings
	if !s.Open || s.Tab != "mcp" || s.MCPEditorOpen || !l.Connected || l.Client == nil || l.ctx.Err() != nil || a.mcpStatusBusy() {
		if l.MCPStatus.Cancel != nil {
			a.cancelMCPStatus()
		}
		return
	}
	if l.MCPStatus.Workspace != s.Workspace {
		a.loadMCPStatus(false)
		return
	}
	if l.MCPStatus.Cancel == nil && !time.Now().Before(l.MCPStatus.NextPoll) {
		a.loadMCPStatus(true)
	}
}

func (a *preview) loadMCPStatus(background bool) {
	l := a.Live
	a.cancelMCPStatus()
	workspace := a.Settings.Workspace
	if l.MCPStatus.Workspace != workspace {
		delete(l.Data, "mcp")
		delete(l.ResourceErrors, "mcp")
	}
	l.MCPStatus.Workspace = workspace
	if !background {
		delete(l.ResourceErrors, "mcp")
	}
	key := "mcp"
	if background {
		key = "mcp-poll"
	}
	l.Loading[key] = true
	ctx, cancel := context.WithTimeout(l.hostCtx, 10*time.Second)
	l.MCPStatus.Cancel = cancel
	epoch, version, b := l.epoch, l.Versions["mcp"], l.Client
	go func() {
		defer cancel()
		var value any
		err := b.request(ctx, http.MethodGet, "/mcp"+query(map[string]string{"workspace": workspace}), nil, &value)
		a.post(epoch, func() {
			if l.Versions["mcp"] != version {
				return
			}
			l.Loading[key] = false
			l.MCPStatus.Cancel = nil
			l.MCPStatus.NextPoll = time.Now().Add(mcpStatusInterval)
			if a.Settings.Workspace != workspace || a.mcpStatusBusy() {
				return
			}
			if err != nil {
				l.ResourceErrors["mcp"] = err.Error()
				if !background || errors.Is(err, errUnauthorized) {
					a.reportLiveError(err)
				}
				return
			}
			a.applyMCPStatus(workspace, value)
		})
	}()
}

// A completed edit/reconnect is newer than any status read already in flight.
func (a *preview) applyMCPStatus(workspace string, value any) {
	if a.Settings.Workspace != workspace {
		return
	}
	a.cancelMCPStatus()
	l := a.Live
	l.MCPStatus.Workspace = workspace
	l.MCPStatus.NextPoll = time.Now().Add(mcpStatusInterval)
	l.Data["mcp"] = value
	delete(l.ResourceErrors, "mcp")
}
