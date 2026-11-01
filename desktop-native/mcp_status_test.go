package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func mcpStatusFixture(workspace, state string) map[string]any {
	count := 0
	if state == "connected" {
		count = 5
	}
	return map[string]any{"workspace": workspace, "project_trusted": true, "servers": []any{
		map[string]any{"name": "cc4-cloud", "scope": "project", "transport": "http", "enabled": true, "trusted": true, "state": state, "tool_count": count, "tools": []any{}},
	}}
}

func mcpState(a *preview) string {
	servers := list(object(a.Live.Data["mcp"])["servers"])
	if len(servers) == 0 {
		return ""
	}
	return str(object(servers[0])["state"])
}

func TestMCPSettingsObserveBackgroundConnectionWithoutReconnect(t *testing.T) {
	var reads, mutations atomic.Int32
	workspace := "/project with spaces"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/mcp" {
			mutations.Add(1)
			http.Error(w, "unexpected mutation", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("workspace") != workspace {
			t.Error("status request lost workspace")
		}
		state := "connected"
		if reads.Add(1) == 1 {
			state = "disconnected" // First GET races the asynchronous MCP handshake.
		}
		_ = json.NewEncoder(w).Encode(mcpStatusFixture(workspace, state))
	}))
	t.Cleanup(server.Close)
	a := alignmentPreview(t)
	a.Live.Connected = true
	a.Live.Client, _ = newBackendClient(server.URL, "")
	a.Settings.Workspace = workspace
	a.Live.SavedMCPForm.Workspace = workspace
	a.openSettings("mcp")
	waitNative(t, a, func() bool { return mcpState(a) == "disconnected" })
	tt := ui.NewTester(a.View, 900, 820)
	click(t, tt, "服务详情：cc4-cloud")
	deadline := time.Now().Add(4 * time.Second)
	for mcpState(a) != "connected" && time.Now().Before(deadline) {
		a.liveTick()
		time.Sleep(5 * time.Millisecond)
	}
	if mcpState(a) != "connected" {
		t.Fatal("MCP list kept the startup snapshot after the backend connected")
	}
	tt.Frame()
	if !tt.HasText("项目 · HTTP · 5 个工具") || !tt.HasText("重新连接") {
		t.Fatal("status refresh lost tool count or expanded server details")
	}
	if mutations.Load() != 0 {
		t.Fatal("observing connection status must not reconnect or change configuration")
	}
}

func TestMCPStatusPollingStopsWhenInactive(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*preview)
	}{
		{"close-settings", func(a *preview) { a.closeSettings() }},
		{"change-page", func(a *preview) { a.openSettings("appearance") }},
		{"edit-server", func(a *preview) { a.beginMCPEditor(nil) }},
		{"reconnect-server", func(a *preview) { a.Live.Loading["mcp-action"] = true }},
		{"host-disconnected", func(a *preview) { a.Live.Connected = false }},
	} {
		t.Run(change.name, func(t *testing.T) {
			started, canceled := make(chan struct{}, 1), make(chan struct{}, 1)
			var reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				started <- struct{}{}
				<-r.Context().Done()
				canceled <- struct{}{}
			}))
			t.Cleanup(server.Close)
			a := alignmentPreview(t)
			a.Live.Connected = true
			a.Live.Client, _ = newBackendClient(server.URL, "")
			a.Settings.Open, a.Settings.Tab = true, "mcp"
			a.applyMCPStatus(a.Settings.Workspace, mcpStatusFixture(a.Settings.Workspace, "connected"))
			a.Live.MCPStatus.NextPoll = time.Time{}
			a.tickMCPStatus()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("background status read did not start")
			}
			if a.Live.Loading["mcp"] {
				t.Fatal("background status read disabled the server controls")
			}
			change.apply(a)
			a.liveTick()
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("hidden/inactive MCP page kept its request alive")
			}
			for i := 0; i < 50; i++ {
				a.liveTick()
			}
			if reads.Load() != 1 || a.Live.Error != "" || a.Live.ResourceErrors["mcp"] != "" {
				t.Fatal("inactive page polled again or surfaced an intentional cancellation")
			}
		})
	}
}

func TestMCPStatusIgnoresReadsOlderThanEditsOrWorkspaceChanges(t *testing.T) {
	for _, changeWorkspace := range []bool{false, true} {
		name := "completed-reconnect"
		if changeWorkspace {
			name = "another-project"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(mcpStatusFixture(r.URL.Query().Get("workspace"), "disconnected"))
			}))
			t.Cleanup(server.Close)
			a := alignmentPreview(t)
			a.Live.Connected = true
			a.Live.Client, _ = newBackendClient(server.URL, "")
			a.Settings.Open, a.Settings.Tab = true, "mcp"
			a.Settings.Workspace = "/old-project"
			a.applyMCPStatus(a.Settings.Workspace, mcpStatusFixture(a.Settings.Workspace, "connecting"))
			a.loadMCPStatus(true)
			// Let the old snapshot reach the UI queue without applying it yet.
			deadline := time.Now().Add(time.Second)
			for len(a.Live.results) == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if len(a.Live.results) == 0 {
				t.Fatal("old status read did not complete")
			}
			if changeWorkspace {
				a.Settings.Workspace = "/new-project"
			}
			a.applyMCPStatus(a.Settings.Workspace, mcpStatusFixture(a.Settings.Workspace, "connected"))
			a.liveTick()
			if mcpState(a) != "connected" || str(object(a.Live.Data["mcp"])["workspace"]) != a.Settings.Workspace {
				t.Fatal("old read overwrote a newer operation/project snapshot")
			}
			if changeWorkspace {
				a.applyMCPStatus("/old-project", mcpStatusFixture("/old-project", "disabled"))
				if mcpState(a) != "connected" {
					t.Fatal("late mutation from the old project replaced the current project")
				}
			}
		})
	}
}

func TestMCPStatusPollingShowsFailuresAndRecovers(t *testing.T) {
	var unavailable atomic.Bool
	unavailable.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
			return
		}
		data := mcpStatusFixture(r.URL.Query().Get("workspace"), "needs_auth")
		object(list(data["servers"])[0])["error"] = "Sign-in required"
		_ = json.NewEncoder(w).Encode(data)
	}))
	t.Cleanup(server.Close)
	a := alignmentPreview(t)
	a.Live.Connected = true
	a.Live.Client, _ = newBackendClient(server.URL, "")
	a.Settings.Open, a.Settings.Tab = true, "mcp"
	a.applyMCPStatus(a.Settings.Workspace, mcpStatusFixture(a.Settings.Workspace, "connected"))
	a.Live.MCPStatus.NextPoll = time.Time{}
	a.tickMCPStatus()
	waitNative(t, a, func() bool { return a.Live.ResourceErrors["mcp"] != "" })
	tt := ui.NewTester(a.View, 900, 820)
	_, staleServer := tt.Find("服务详情：cc4-cloud")
	if !tt.HasText("重试") || staleServer {
		t.Fatal("failed status read presented stale server state as current")
	}
	unavailable.Store(false)
	a.Live.MCPStatus.NextPoll = time.Time{}
	waitNative(t, a, func() bool { return mcpState(a) == "needs_auth" })
	tt.Frame()
	if a.Live.ResourceErrors["mcp"] != "" || !tt.HasText("需要授权") || !tt.HasText("Sign-in required") {
		t.Fatal("automatic refresh hid the server's real authentication failure")
	}
}

func TestNativeCoreMCPStartupStatusSettlesWithoutReconnect(t *testing.T) {
	a, _, _, _ := nativeCoreFixture(t)
	ready := make(chan struct{})
	var initialized atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var rpc struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			http.Error(w, "invalid JSON-RPC", http.StatusBadRequest)
			return
		}
		if len(rpc.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch rpc.Method {
		case "initialize":
			initialized.Add(1)
			result = map[string]any{"protocolVersion": rpc.Params.ProtocolVersion, "serverInfo": map[string]any{"name": "startup-fixture", "version": "1"}, "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			select {
			case <-ready:
			case <-r.Context().Done():
				return
			}
			result = map[string]any{"tools": []any{map[string]any{"name": "fixture_read", "description": "Read fixture", "inputSchema": map[string]any{"type": "object"}}}}
		default:
			http.Error(w, "unexpected MCP request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
	}))
	// Stop the test core before closing the fixture's possible open requests.
	t.Cleanup(func() { a.closeLive(); server.Close() })
	configPath := filepath.Join(a.Live.Dir, "core-home", "mcp.json")
	if err := atomicJSON(configPath, map[string]any{"mcpServers": map[string]any{"cc4-cloud": map[string]any{"url": server.URL, "trust": true}}}); err != nil {
		t.Fatal(err)
	}
	a.openSettings("mcp")
	waitNative(t, a, func() bool { return mcpState(a) == "connecting" || mcpState(a) == "disconnected" })
	close(ready)
	waitNative(t, a, func() bool { return mcpState(a) == "connected" })
	if initialized.Load() != 1 || number(object(list(object(a.Live.Data["mcp"])["servers"])[0])["tool_count"]) != 1 {
		t.Fatal("startup connection was restarted or its tools were not projected")
	}
	if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, ui.NewTester(a.View, 900, 820), filepath.Join(dir, "mcp-startup-connected.png"))
	}
}
