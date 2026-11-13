package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestUpdaterTransportModes(t *testing.T) {
	direct, err := updaterTransport(updateProxyDirect, "")
	if err != nil {
		t.Fatal(err)
	}
	if direct.Proxy != nil {
		t.Fatal("direct mode should not set a proxy")
	}
	custom, err := updaterTransport(updateProxyCustom, "http://127.0.0.1:7890")
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := custom.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "github.com"}})
	if err != nil || proxy == nil || proxy.String() != "http://127.0.0.1:7890" {
		t.Fatalf("custom mode proxy = %v, err = %v", proxy, err)
	}
	if _, err := updaterTransport(updateProxyCustom, "socks5://127.0.0.1:1080"); err != nil {
		t.Fatalf("socks5 proxy should be accepted: %v", err)
	}
	for _, bad := range []string{"", "not a url", "ftp://127.0.0.1:21", "http://"} {
		if _, err := updaterTransport(updateProxyCustom, bad); err == nil {
			t.Fatalf("custom mode should reject %q", bad)
		}
	}
	if _, err := updaterTransport("未知模式", ""); err == nil {
		t.Fatal("unknown mode should be rejected")
	}
}

func TestProxyUpdateSourceServesFeedAndAssets(t *testing.T) {
	manifest := `{"version":"0.46.0","url":"ASSET_URL","signature":"sig"}`
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "mygo-updater" {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		switch {
		case strings.HasSuffix(r.URL.Path, ".json"):
			w.Write([]byte(strings.Replace(manifest, "ASSET_URL", server.URL+"/asset.tar.gz", 1)))
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			w.Write([]byte("payload"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oldBase := updateFeedBase
	updateFeedBase = server.URL
	defer func() { updateFeedBase = oldBase }()

	transport, err := updaterTransport(updateProxyDirect, "")
	if err != nil {
		t.Fatal(err)
	}
	source := proxyUpdateSource{get: proxyGet(transport)}
	body, err := source.Manifest(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version":"0.46.0"`) {
		t.Fatalf("manifest = %s", data)
	}
	if got := updateFeedURL(); !strings.HasSuffix(got, "/update-"+runtime.GOOS+"-"+runtime.GOARCH+".json") {
		t.Fatalf("updateFeedURL = %s", got)
	}
	archive, err := source.Open(context.Background(), server.URL+"/asset.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(archive)
	archive.Close()
	if err != nil || string(payload) != "payload" {
		t.Fatalf("archive payload = %q, err = %v", payload, err)
	}
}

func TestProxyGetRejectsNonHTTPS(t *testing.T) {
	transport, err := updaterTransport(updateProxyDirect, "")
	if err != nil {
		t.Fatal(err)
	}
	get := proxyGet(transport)
	if _, err := get(context.Background(), "http://example.com/update.json"); err == nil || !strings.Contains(err.Error(), "updates need HTTPS") {
		t.Fatalf("non-loopback http should be refused, err = %v", err)
	}
}

func TestProxyGetReportsNonOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer server.Close()
	transport, err := updaterTransport(updateProxyDirect, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = proxyGet(transport)(context.Background(), server.URL+"/x.json")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("non-200 should error, err = %v", err)
	}
}

func TestApplyUpdateProxySmoke(t *testing.T) {
	a := alignmentPreview(t)
	for _, mode := range []string{"", updateProxySystem, updateProxyDirect, updateProxyCustom} {
		a.Settings.Preferences.UpdateProxyMode = mode
		a.Settings.Preferences.UpdateProxyURL = "http://127.0.0.1:1"
		a.applyUpdateProxy()
	}
}

func TestUpdateProxyPreferencePersistence(t *testing.T) {
	a := alignmentPreview(t)
	s := &a.Settings
	s.Path = t.TempDir() + "/native-preview-preferences.json"
	s.Preferences.UpdateProxyMode = updateProxyCustom
	s.Preferences.UpdateProxyURL = "http://127.0.0.1:7890"
	if !s.savePreferences() {
		t.Fatal(s.Error)
	}
	recovered := &settingsState{}
	recovered.loadPreferences(s.Path)
	if recovered.Preferences.UpdateProxyMode != updateProxyCustom || recovered.Preferences.UpdateProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("recovered = %q / %q", recovered.Preferences.UpdateProxyMode, recovered.Preferences.UpdateProxyURL)
	}
	// A hand-edited unknown mode loads as-is and keeps the rest of the
	// file; applyUpdateProxy treats anything but 直连/自定义代理 as system.
	broken := map[string]any{"theme": "dark", "language": "中文", "update_proxy_mode": "走代理"}
	if data, err := json.Marshal(broken); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(s.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	recovered = &settingsState{}
	recovered.loadPreferences(s.Path)
	if recovered.Preferences.UpdateProxyMode != "走代理" || recovered.Preferences.Theme != "dark" {
		t.Fatalf("broken mode recovered = %q, theme = %q", recovered.Preferences.UpdateProxyMode, recovered.Preferences.Theme)
	}
	// An empty file value (old preferences) round-trips as empty and the
	// dropdown shows the default.
	empty := map[string]any{"theme": "dark", "language": "中文"}
	if data, err := json.Marshal(empty); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(s.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	recovered = &settingsState{}
	recovered.loadPreferences(s.Path)
	if recovered.Preferences.UpdateProxyMode != "" {
		t.Fatalf("empty mode recovered = %q", recovered.Preferences.UpdateProxyMode)
	}
}
