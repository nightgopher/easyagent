package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/gorilla/websocket"
)

type droppingHostSecrets struct{ memorySecrets }

func (s *droppingHostSecrets) Set(string, string) error { return nil }

func hostCredentialPreview(t *testing.T, store secretStore) *preview {
	t.Helper()
	a := newConnectedPreview(t.TempDir())
	a.Live.Secrets = store
	a.Live.Config.Hosts = []hostConfig{{ID: "local", Name: "Local", Local: true}, {ID: "remote", Name: "Remote", URL: "http://127.0.0.1:1", HasToken: true}}
	a.Live.Config.ActiveHost = "local"
	a.syncSettingsConfig()
	a.Settings.EditHost = "remote"
	a.Settings.HostName = "Remote"
	a.Settings.HostURL = "http://127.0.0.1:1"
	t.Cleanup(func() {
		if a.Live.ctx.Err() == nil {
			a.closeLive()
		}
	})
	return a
}

func TestHostMissingCredentialFailsBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); fmt.Fprint(w, `{}`) }))
	defer server.Close()
	a := hostCredentialPreview(t, &memorySecrets{})
	a.Live.Config.Hosts[1].URL = server.URL
	a.Live.Config.ActiveHost = "remote"
	a.syncSettingsConfig()
	a.connectLive()
	waitNative(t, a, func() bool { return !a.Live.Connecting })
	if a.Live.Client != nil || requests.Load() != 0 || !strings.Contains(a.Live.Error, "令牌") {
		t.Fatalf("missing credential reached network or lacked repair guidance: client=%t requests=%d error=%q", a.Live.Client != nil, requests.Load(), a.Live.Error)
	}
}

func TestHostSaveVerifiesCredentialBeforeWritingProfile(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			a := hostCredentialPreview(t, &droppingHostSecrets{})
			if !existing {
				a.Settings.EditHost = ""
			}
			a.Settings.HostToken = "synthetic-host-credential"
			a.saveLiveHost()
			waitNative(t, a, func() bool { return !a.Live.Loading["host-save"] })
			if a.Live.Error == "" || len(a.Live.Config.Hosts) != 2 {
				t.Fatal("unreadable secret was marked saved")
			}
			if _, err := os.Stat(filepath.Join(a.Live.Dir, "connections.json")); !os.IsNotExist(err) {
				t.Fatal("unverified profile reached disk")
			}
			if a.Settings.HostToken == "" {
				t.Fatal("failed save lost the entered credential")
			}
		})
	}
}

func TestHostBlankTokenRequiresReadableSavedCredential(t *testing.T) {
	a := hostCredentialPreview(t, &memorySecrets{})
	a.saveLiveHost()
	waitNative(t, a, func() bool { return !a.Live.Loading["host-save"] })
	if !strings.Contains(a.Live.Error, "令牌") || len(a.Live.Config.Hosts) != 2 {
		t.Fatal("blank form silently retained a missing credential")
	}
}

func TestHostSavingSameCredentialKeepsIdentity(t *testing.T) {
	store := &memorySecrets{values: map[string]string{"host/remote": "synthetic-host-credential"}}
	a := hostCredentialPreview(t, store)
	a.Settings.HostName = "Renamed"
	a.Settings.HostToken = "synthetic-host-credential"
	a.saveLiveHost()
	waitNative(t, a, func() bool { return !a.Live.Loading["host-save"] })
	if a.Live.Error != "" || len(a.Live.Config.Hosts) != 2 || a.Settings.EditHost != "remote" {
		t.Fatal("unchanged credential created a duplicate identity", a.Live.Error)
	}
	data, err := os.ReadFile(filepath.Join(a.Live.Dir, "connections.json"))
	if err != nil || strings.Contains(string(data), "synthetic-host-credential") {
		t.Fatal("profile save failed or leaked the credential")
	}
}

func TestHostAddressChangeRequiresExplicitCredential(t *testing.T) {
	store := &memorySecrets{values: map[string]string{"host/remote": "synthetic-host-credential"}}
	a := hostCredentialPreview(t, store)
	a.Settings.HostURL = "http://127.0.0.1:2"
	a.saveLiveHost()
	waitNative(t, a, func() bool { return !a.Live.Loading["host-save"] })
	if a.Live.Error == "" || len(a.Live.Config.Hosts) != 2 {
		t.Fatal("authenticated endpoint changed without an explicit credential")
	}
}

func TestHostSaveCleansFreshCredentialOnPersistenceFailure(t *testing.T) {
	store := &memorySecrets{values: map[string]string{"host/remote": "previous-synthetic-credential"}}
	a := hostCredentialPreview(t, store)
	a.Settings.EditHost = ""
	a.Settings.HostToken = "fresh-synthetic-credential"
	if err := os.Mkdir(filepath.Join(a.Live.Dir, "connections.json"), 0700); err != nil {
		t.Fatal(err)
	}
	a.saveLiveHost()
	waitNative(t, a, func() bool { return !a.Live.Loading["host-save"] })
	store.mu.Lock()
	defer store.mu.Unlock()
	if a.Live.Error == "" || len(a.Live.Config.Hosts) != 2 || len(store.values) != 1 || store.values["host/remote"] != "previous-synthetic-credential" || a.Settings.HostToken == "" {
		t.Fatal("failed persistence left a credential or lost the original state")
	}
}

func TestHostRemovalCleansOnlyItsCredential(t *testing.T) {
	store := &memorySecrets{values: map[string]string{"host/remote": "synthetic-host-credential", "host/other": "other-synthetic-credential"}}
	a := hostCredentialPreview(t, store)
	a.removeLiveHost(1)
	waitNative(t, a, func() bool { return !a.Live.Loading["configuration"] })
	if a.Live.Error != "" || len(a.Live.Config.Hosts) != 1 {
		t.Fatal("removal failed", a.Live.Error)
	}
	removed, _ := store.Get("host/remote")
	retained, _ := store.Get("host/other")
	if removed != "" || retained == "" {
		t.Fatal("removed credential remains or another connection was modified")
	}
}

func TestHostRemovalRestoresCredentialOnPersistenceFailure(t *testing.T) {
	store := &memorySecrets{values: map[string]string{"host/remote": "synthetic-host-credential"}}
	a := hostCredentialPreview(t, store)
	if err := os.Mkdir(filepath.Join(a.Live.Dir, "connections.json"), 0700); err != nil {
		t.Fatal(err)
	}
	a.removeLiveHost(1)
	waitNative(t, a, func() bool { return !a.Live.Loading["configuration"] })
	restored, _ := store.Get("host/remote")
	if a.Live.Error == "" || len(a.Live.Config.Hosts) != 2 || restored != "synthetic-host-credential" {
		t.Fatal("failed removal lost config or credential")
	}
}

func TestHostWebSocketUnauthorizedStopsAndOffersRepair(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			attempts.Add(1)
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/sessions" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	a := hostCredentialPreview(t, &memorySecrets{})
	a.Live.Config.Hosts[1] = hostConfig{ID: "remote", Name: "Remote", URL: server.URL}
	a.Live.Config.ActiveHost = "remote"
	a.syncSettingsConfig()
	a.connectLive()
	waitNative(t, a, func() bool { return strings.Contains(a.Live.Error, "401") })
	for end := time.Now().Add(1200 * time.Millisecond); time.Now().Before(end); {
		a.liveTick()
		time.Sleep(5 * time.Millisecond)
	}
	if attempts.Load() != 1 {
		t.Fatal("401 retried without a replacement credential")
	}
}

func TestHostAuthenticationErrorSurvivesLateOperations(t *testing.T) {
	for _, status := range []int{200, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-release
				w.WriteHeader(status)
				fmt.Fprint(w, `{}`)
			}))
			t.Cleanup(server.Close)
			a := hostCredentialPreview(t, &memorySecrets{})
			a.Live.Config.ActiveHost = "remote"
			a.syncSettingsConfig()
			a.Live.Client, _ = newBackendClient(server.URL, "")
			a.operation("late-operation", "GET", "/slow", nil, nil)
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("operation did not start")
			}
			a.reportLiveError(errUnauthorized)
			once.Do(func() { close(release) })
			waitNative(t, a, func() bool { return !a.Live.Loading["late-operation"] })
			if !a.Live.CredentialRepair || !strings.Contains(a.Live.Error, "401") {
				t.Fatal("late response overwrote the authentication failure", a.Live.Error)
			}
		})
	}
}

func TestSavedHostCredentialAuthenticatesRESTAndWSAfterRestart(t *testing.T) {
	testSavedHostAuthentication(t, &memorySecrets{})
}

func testSavedHostAuthentication(t *testing.T, store secretStore) {
	t.Helper()
	const credential = "synthetic-host-credential"
	var mu sync.Mutex
	counts := map[string]int{}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" && r.Header.Get("Authorization") != "Bearer "+credential {
			t.Errorf("missing authentication for %s", r.URL.Path)
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		counts[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		case "/sessions":
			fmt.Fprint(w, `[]`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(server.Close)
	a := hostCredentialPreview(t, store)
	a.Settings.EditHost = ""
	a.Settings.HostURL = server.URL
	a.Settings.HostToken = credential
	a.saveLiveHost()
	waitNative(t, a, func() bool { return !a.Live.Loading["host-save"] })
	if a.Live.Error != "" {
		t.Fatal(a.Live.Error)
	}
	t.Log("saved authenticated host profile")
	index := len(a.Live.Config.Hosts) - 1
	a.selectLiveHost(index)
	waitNative(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return a.Live.Connected && !a.Live.Loading["configuration"] && counts["/models"] >= 1 && counts["/sessions"] >= 1 && counts["/ws"] >= 1
	})
	t.Log("first authenticated connection established")
	first := a.Live.Config.ActiveHost
	t.Cleanup(func() {
		if err := store.Delete("host/" + first); err != nil {
			t.Errorf("synthetic host credential cleanup failed: %v", err)
		}
	})
	dir := a.Live.Dir
	a.closeLive()
	b := newConnectedPreview(dir)
	defer b.closeLive()
	b.Live.Secrets = store
	b.connectLive()
	t.Log("reconnecting persisted profile")
	waitNative(t, b, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return b.Live.Connected && counts["/models"] >= 2 && counts["/sessions"] >= 2 && counts["/ws"] >= 2
	})
	var cfg clientConfig
	data, _ := os.ReadFile(filepath.Join(dir, "connections.json"))
	_ = json.Unmarshal(data, &cfg)
	if cfg.ActiveHost != first || b.Live.Client.Token != credential {
		t.Fatal("restart lost the saved host or credential")
	}
}

func TestHostCredentialRepairPreservesDirtyFormAndFocusesToken(t *testing.T) {
	a := hostCredentialPreview(t, &memorySecrets{})
	a.Live.Config.ActiveHost = "remote"
	a.syncSettingsConfig()
	a.openSettings("mcp")
	a.Settings.MCPName = "unfinished"
	a.reportLiveError(errUnauthorized)
	a.repairLiveCredential()
	if a.Live.Confirm == nil || a.Settings.Tab != "mcp" || a.Settings.MCPName != "unfinished" {
		t.Fatal("repair discarded another form without confirmation")
	}
	a.Live.Confirm.Action()
	a.Live.Confirm = nil
	if a.Settings.Tab != "connections" || a.Settings.EditHost != "remote" || a.Settings.HostToken != "" {
		t.Fatal("repair opened the wrong profile")
	}
	tt := ui.NewTester(a.View, 1240, 820)
	if !tt.Focused("访问令牌") {
		t.Fatal("repair did not focus the credential field")
	}
}

func TestHostCredentialRepairReconnectsAndKeepsPreviousDraft(t *testing.T) {
	const credential = "replacement-synthetic-credential"
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" && r.Header.Get("Authorization") != "Bearer "+credential {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				if _, _, err = conn.ReadMessage(); err != nil {
					return
				}
			}
		case "/sessions":
			fmt.Fprint(w, `[]`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(server.Close)
	store := &memorySecrets{values: map[string]string{"host/remote": "previous-synthetic-credential"}}
	a := hostCredentialPreview(t, store)
	a.Live.Config.Hosts[1].URL = server.URL
	a.Live.Config.ActiveHost = "remote"
	a.syncSettingsConfig()
	a.current().Draft = "draft before authentication repair"
	a.connectLive()
	waitNative(t, a, func() bool { return a.Live.CredentialRepair })
	a.repairLiveCredential()
	a.Settings.HostToken = credential
	a.saveLiveHost()
	waitNative(t, a, func() bool { return a.Live.Connected && !a.Live.Loading["configuration"] })
	if a.Live.CredentialRepair || a.Live.Error != "" || a.Live.Client.Token != credential || a.Live.Config.ActiveHost == "remote" || a.Live.Drafts["remote/"] != "draft before authentication repair" {
		t.Fatal("credential repair lost isolation, drafts or connectivity")
	}
}
