package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestRemoteAccessPairingUIAndSecretCleanup(t *testing.T) {
	a := alignmentPreview(t)
	a.Settings.Hosts = []demoHost{{ID: "local", Name: "此 Mac", Local: true}}
	a.Settings.Host = 0
	a.Settings.Open, a.Settings.Tab = true, "remote-access"
	a.Live.Data["remote-access"] = map[string]any{"enabled": true, "online": true, "relay_url": "https://relay.example", "pairing": true, "devices": []any{map[string]any{"id": "device-1", "name": "我的手机"}}}
	var cancelled atomic.Int32
	var pairing atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/remote-access" {
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "online": true, "relay_url": "https://relay.example", "pairing": pairing.Load()})
			return
		}
		if r.Method == "DELETE" && r.URL.Path == "/remote-access/pair" {
			cancelled.Add(1)
			fmt.Fprint(w, `{}`)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/remote-access/pair" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
		pairing.Store(true)
		_ = json.NewEncoder(w).Encode(map[string]any{"v": 1, "relay_url": "https://relay.example", "route_id": strings.Repeat("a", 43), "host_pubkey_fp": "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "pair_token": strings.Repeat("b", 43), "host_name": "此 Mac", "expires_at": time.Now().Add(5 * time.Minute).Format(time.RFC3339)})
	}))
	defer backend.Close()
	a.Live.Client, _ = newBackendClient(backend.URL, "owner-secret-never-in-qr")
	tt := ui.NewTester(a.View, 1080, 1050)
	if !tt.HasText("已连接 · 可从手机访问") || !tt.HasText("我的手机") {
		t.Fatal("remote access status or devices missing")
	}
	click(t, tt, "生成配对二维码")
	waitNative(t, a, func() bool { return !a.Live.Loading["remote-pair"] })
	tt.Frame()
	if a.Live.RemoteAccess.Bitmap == nil || strings.Contains(a.Live.RemoteAccess.Payload, "owner-secret") {
		t.Fatal("pair QR missing or leaked owner key")
	}
	if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, tt, filepath.Join(dir, "remote-access.png"))
	}
	a.Settings.RelayRegistrationKey = "registration-secret"
	a.closeSettings()
	if a.Live.RemoteAccess.Payload != "" || a.Live.RemoteAccess.Bitmap != nil || a.Settings.RelayRegistrationKey != "" {
		t.Fatal("pairing secrets survived closing settings")
	}
	deadline := time.Now().Add(time.Second)
	for cancelled.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if cancelled.Load() != 1 {
		t.Fatal("pairing was not cancelled on the host")
	}
}

func TestRemoteAccessCanOnlyBeConfiguredOnLocalHost(t *testing.T) {
	a := alignmentPreview(t)
	a.Settings.Hosts = []demoHost{{ID: "remote", Name: "远程主机", Local: false}}
	a.Settings.Host = 0
	a.Settings.Open, a.Settings.Tab = true, "remote-access"
	tt := ui.NewTester(a.View, 1000, 900)
	if tt.HasText("开启远程访问") || !tt.HasText("请在运行 EasyAgent 的那台电脑上打开此设置，生成手机配对二维码。") {
		t.Fatal("remote host exposed pairing controls")
	}
}
