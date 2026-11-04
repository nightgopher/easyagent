package easycode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFeishuLoginUsesStateAndSavesAccount(t *testing.T) {
	var exchanges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.UserAgent(); got != "EasyAgent" {
			t.Errorf("client identity = %q", got)
		}
		switch r.URL.Path {
		case "/api/config/client":
			fmt.Fprint(w, `{"success":true,"data":{"feishu":{"appId":"app-default"},"feishuTenants":[{"appId":"app-default","label":"Default"},{"appId":"app-team","label":"Team"}]}}`)
		case "/api/auth/feishu/exchange":
			exchanges.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["code"] != "one-use-code" || body["app_id"] != "app-team" || !strings.Contains(body["redirect_uri"], "/callback") {
				t.Errorf("wrong exchange metadata: %+v", body)
			}
			fmt.Fprint(w, `{"success":true,"data":{"accessToken":"feishu-token-fixture"}}`)
		case "/auth/jwt/feishu-login":
			var body struct {
				FeishuAccessToken string `json:"feishuAccessToken"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.FeishuAccessToken != "feishu-token-fixture" {
				t.Error("missing exchanged credential")
			}
			fmt.Fprint(w, `{"accessToken":"access-fixture","refreshToken":"refresh-fixture","expiresIn":900,"user":{"userId":"user-1","name":"Alice"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m, err := NewManager(t.TempDir(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	m.ports = []int{0}
	session, err := m.StartLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	page, err := client.Get(session.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = page.Body.Close()
	if page.StatusCode != 200 {
		t.Fatalf("login page: %d", page.StatusCode)
	}
	start, _ := url.Parse(session.URL)
	q := start.Query()
	q.Set("tenant", "1")
	start.Path = "/start"
	start.RawQuery = q.Encode()
	redirect, err := client.Get(start.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = redirect.Body.Close()
	if redirect.StatusCode != http.StatusFound {
		t.Fatalf("authorization redirect: %d", redirect.StatusCode)
	}
	authorize, err := url.Parse(redirect.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if authorize.Host != "open.feishu.cn" || authorize.Query().Get("app_id") != "app-team" {
		t.Fatalf("unexpected authorization URL: %s", authorize.Host)
	}
	callback, err := url.Parse(authorize.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	callback.Host = start.Host // connect over IPv4 while keeping the registered redirect URI unchanged
	cq := callback.Query()
	cq.Set("code", "one-use-code")
	cq.Set("state", "wrong-state")
	callback.RawQuery = cq.Encode()
	invalid, err := client.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = invalid.Body.Close()
	if invalid.StatusCode != http.StatusBadRequest || exchanges.Load() != 0 {
		t.Fatal("invalid state reached token exchange")
	}
	cq.Set("state", authorize.Query().Get("state"))
	callback.RawQuery = cq.Encode()
	valid, err := client.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = valid.Body.Close()
	if valid.StatusCode != http.StatusOK {
		t.Fatalf("valid callback status %d", valid.StatusCode)
	}
	user, err := session.Wait(context.Background())
	if err != nil || user.Name != "Alice" || exchanges.Load() != 1 {
		t.Fatalf("login: user=%+v, err=%v, exchanges=%d", user, err, exchanges.Load())
	}
	info, err := os.Stat(filepath.Join(m.dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("credential file permissions %o", info.Mode().Perm())
	}
	got, err := m.AccessToken(context.Background())
	if err != nil || got != "access-fixture" {
		t.Fatalf("saved token unavailable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "credentials.json")); err != nil {
		t.Fatal(err)
	}
	if err := m.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AccessToken(context.Background()); err == nil {
		t.Fatal("logged out credential remained usable")
	}
}

func TestRefreshIsSerializedAndPersistsRotatedCredentials(t *testing.T) {
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/jwt/refresh" {
			http.NotFound(w, r)
			return
		}
		refreshes.Add(1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["refreshToken"] != "old-refresh" {
			t.Error("unexpected refresh credential")
		}
		time.Sleep(25 * time.Millisecond)
		fmt.Fprint(w, `{"success":true,"data":{"accessToken":"new-access","refreshToken":"new-refresh","expiresIn":900}}`)
	}))
	defer server.Close()
	home := t.TempDir()
	a, _ := NewManager(home, server.URL)
	b, _ := NewManager(home, server.URL)
	if err := a.save(context.Background(), credentials{AccessToken: "expired", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute).UnixMilli(), User: User{Name: "Alice"}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := a
			if i%2 == 0 {
				m = b
			}
			token, err := m.AccessToken(context.Background())
			if err != nil || token != "new-access" {
				t.Errorf("refresh: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("expected one refresh, got %d", refreshes.Load())
	}
	saved, err := a.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if saved.RefreshToken != "new-refresh" || saved.User.Name != "Alice" {
		t.Fatal("rotated credentials or account were not preserved")
	}
}

func TestTransientRefreshFailureRetainsCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	m, _ := NewManager(t.TempDir(), server.URL)
	want := credentials{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute).UnixMilli()}
	if err := m.save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AccessToken(context.Background()); err == nil {
		t.Fatal("expired token must not be used")
	}
	got, err := m.load(context.Background())
	if err != nil || got.RefreshToken != want.RefreshToken {
		t.Fatal("transient failure erased refresh credential")
	}
}

func TestUnsafeRemoteServerIsRejected(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/?key=secret"} {
		if _, err := NewManager(t.TempDir(), raw); err == nil {
			t.Errorf("accepted unsafe URL %s", strings.Split(raw, "?")[0])
		}
	}
}

func TestCredentialSymlinkIsNotFollowed(t *testing.T) {
	home := t.TempDir()
	m, err := NewManager(home, "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(m.dir, 0700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "elsewhere.json")
	if err = os.WriteFile(other, []byte(`{"accessToken":"other","expiresAt":4102444800000}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(other, filepath.Join(m.dir, "credentials.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = m.AccessToken(context.Background()); err == nil {
		t.Fatal("followed a credential symlink")
	}
}
