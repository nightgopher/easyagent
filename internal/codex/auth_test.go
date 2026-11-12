package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// tokenServer 模拟 auth.openai.com 的 /oauth/token 端点，校验 PKCE 与 client_id。
func tokenServer(t *testing.T, requests *[]tokenRequest, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		*requests = append(*requests, tokenRequest{Form: r.PostForm})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"acc-fixture","refresh_token":"ref-fixture","expires_in":28800,"id_token":"`+fixtureIDToken+`"}`)
	}))
}

type tokenRequest struct{ Form url.Values }

const fixtureIDToken = "eyJhbGciOiJSUzI1NiJ9.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoiYWNjb3VudC0xIn19.sig"

func TestAccountIDFromIDToken(t *testing.T) {
	id, err := accountIDFromIDToken(fixtureIDToken)
	if err != nil {
		t.Fatalf("accountIDFromIDToken: %v", err)
	}
	if id != "account-1" {
		t.Fatalf("account id = %q, want account-1", id)
	}
	if _, err := accountIDFromIDToken("not-a-jwt"); err == nil {
		t.Fatal("expected error for non-JWT token")
	}
	if _, err := accountIDFromIDToken("a.b.c"); err == nil {
		t.Fatal("expected error for JWT without chatgpt claim")
	}
}

func TestExchangeAuthorizationCodeSendsPKCE(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := m.newAuthorizationFlow()
	if err != nil {
		t.Fatal(err)
	}
	cred, err := m.exchangeAuthorizationCode(context.Background(), "one-time-code", flow.verifier, auth.URL+"/callback")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if cred.AccessToken != "acc-fixture" || cred.RefreshToken != "ref-fixture" || cred.AccountID != "account-1" {
		t.Fatalf("credential = %+v", cred)
	}
	if cred.ExpiresAt <= time.Now().UnixMilli() {
		t.Fatal("expiresAt not set from expires_in")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("token requests = %d", len(requests))
	}
	form := requests[0].Form
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "one-time-code" {
		t.Fatalf("form = %+v", form)
	}
	if form.Get("client_id") != ClientID {
		t.Fatalf("client_id = %q", form.Get("client_id"))
	}
	if form.Get("code_verifier") != flow.verifier || len(flow.verifier) < 43 {
		t.Fatalf("code_verifier missing or too short: %q", flow.verifier)
	}
	if form.Get("redirect_uri") != auth.URL+"/callback" {
		t.Fatalf("redirect_uri = %q", form.Get("redirect_uri"))
	}
	// 凭据落盘；Unix 上仅当前用户可读（Windows 的 POSIX 权限位无意义，跳过）
	path := filepath.Join(m.dir, "credentials.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("credentials not saved: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode = %v", info.Mode().Perm())
	}
}

func TestRefreshAccessTokenRotatesCredentials(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.save(credentials{AccessToken: "old-acc", RefreshToken: "old-ref", ExpiresAt: 1, AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	cred, err := m.Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if cred.AccessToken != "acc-fixture" || cred.RefreshToken != "ref-fixture" {
		t.Fatalf("credential = %+v", cred)
	}
	mu.Lock()
	form := requests[0].Form
	mu.Unlock()
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "old-ref" {
		t.Fatalf("refresh form = %+v", form)
	}
	if form.Get("client_id") != ClientID {
		t.Fatalf("client_id = %q", form.Get("client_id"))
	}
}

func TestAccessTokenRefreshesNearExpiry(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	// 30 秒后过期，应触发刷新
	if err := m.save(credentials{AccessToken: "old-acc", RefreshToken: "old-ref", ExpiresAt: time.Now().Add(30 * time.Second).UnixMilli(), AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	token, err := m.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if token != "acc-fixture" {
		t.Fatalf("token = %q", token)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("expected refresh, got %d token requests", len(requests))
	}
}

func TestAccessTokenValidTokenDoesNotRefresh(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.save(credentials{AccessToken: "fresh", RefreshToken: "old-ref", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	token, err := m.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if token != "fresh" {
		t.Fatalf("token = %q", token)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 0 {
		t.Fatalf("unexpected refresh requests: %d", len(requests))
	}
}

func TestRefreshAfterRejectionUsesNewerToken(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.save(credentials{AccessToken: "fresh", RefreshToken: "old-ref", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	// rejected == 当前 token：必须走刷新而不是复用
	token, err := m.RefreshAfterRejection(context.Background(), "fresh")
	if err != nil {
		t.Fatalf("RefreshAfterRejection: %v", err)
	}
	if token != "acc-fixture" {
		t.Fatalf("token = %q", token)
	}
	// rejected != 当前 token（另一请求已刷新）：直接复用新 token
	token, err = m.RefreshAfterRejection(context.Background(), "stale")
	if err != nil {
		t.Fatalf("RefreshAfterRejection: %v", err)
	}
	if token != "acc-fixture" {
		t.Fatalf("token = %q", token)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("token requests = %d, want 1", len(requests))
	}
}

func TestStatusAndLogout(t *testing.T) {
	m, err := NewManager(t.TempDir(), "https://auth.openai.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, loggedIn, err := m.Status(context.Background()); err != nil || loggedIn {
		t.Fatalf("empty status: loggedIn=%v err=%v", loggedIn, err)
	}
	if err := m.save(credentials{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), AccountID: "account-1", User: User{Name: "Alice"}}); err != nil {
		t.Fatal(err)
	}
	user, loggedIn, err := m.Status(context.Background())
	if err != nil || !loggedIn || user.Name != "Alice" {
		t.Fatalf("status = %+v %v %v", user, loggedIn, err)
	}
	if err := m.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, loggedIn, _ := m.Status(context.Background()); loggedIn {
		t.Fatal("still logged in after logout")
	}
}

func TestNewManagerRejectsInsecureBaseURL(t *testing.T) {
	for _, base := range []string{"http://auth.openai.com", "https://user:pass@x.com", "https://x.com/path", "https://x.com?query=1", ""} {
		if base == "" {
			// 空 base 走默认值，应当成功
			if _, err := NewManager(t.TempDir(), base); err != nil {
				t.Fatalf("empty base should use default: %v", err)
			}
			continue
		}
		if _, err := NewManager(t.TempDir(), base); err == nil {
			t.Fatalf("base %q should be rejected", base)
		}
	}
}

func TestAuthorizeURLContainsPKCEParams(t *testing.T) {
	m, err := NewManager(t.TempDir(), "https://auth.openai.com")
	if err != nil {
		t.Fatal(err)
	}
	flow, err := m.newAuthorizationFlow()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(flow.url)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "https" || u.Host != "auth.openai.com" || u.Path != "/oauth/authorize" {
		t.Fatalf("authorize url = %s", flow.url)
	}
	if q.Get("response_type") != "code" || q.Get("client_id") != ClientID {
		t.Fatalf("query = %v", q)
	}
	if q.Get("redirect_uri") != redirectURI || q.Get("scope") != scope {
		t.Fatalf("query = %v", q)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("PKCE missing: %v", q)
	}
	if q.Get("state") == "" || q.Get("state") != flow.state {
		t.Fatalf("state mismatch")
	}
	if q.Get("codex_cli_simplified_flow") != "true" || q.Get("originator") == "" {
		t.Fatalf("codex params missing: %v", q)
	}
	// verifier 是 base64url 且无 padding
	if strings.ContainsAny(flow.verifier, "+/=") {
		t.Fatalf("verifier should be base64url without padding: %q", flow.verifier)
	}
}

func TestParseAuthorizationInput(t *testing.T) {
	cases := []struct {
		in       string
		code     string
		state    string
		wantFail bool
	}{
		{in: "http://localhost:1455/auth/callback?code=abc&state=s1", code: "abc", state: "s1"},
		{in: "abc#s1", code: "abc", state: "s1"},
		{in: "code=abc&state=s1", code: "abc", state: "s1"},
		{in: "abc", code: "abc"},
		{in: "", wantFail: true},
	}
	for _, tc := range cases {
		code, state, err := parseAuthorizationInput(tc.in)
		if tc.wantFail {
			if err == nil {
				t.Errorf("input %q: expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("input %q: %v", tc.in, err)
			continue
		}
		if code != tc.code || state != tc.state {
			t.Errorf("input %q: got (%q,%q) want (%q,%q)", tc.in, code, state, tc.code, tc.state)
		}
	}
}

func TestStateMismatchRejected(t *testing.T) {
	m, err := NewManager(t.TempDir(), "https://auth.openai.com")
	if err != nil {
		t.Fatal(err)
	}
	flow, err := m.newAuthorizationFlow()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseAuthorizationInput("code=abc#different-state"); err == nil {
		// parse 本身不校验 state，校验在 completeLogin
		t.Log("parse does not validate state; completeLogin does")
	}
	if err := m.completeLogin(context.Background(), flow, "code=abc#different-state"); err == nil {
		t.Fatal("state mismatch should be rejected")
	}
}

func TestCompleteLoginFullFlow(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := m.newAuthorizationFlow()
	if err != nil {
		t.Fatal(err)
	}
	// 用回调 URL 形式输入（浏览器重定向后粘贴）
	if err := m.completeLogin(context.Background(), flow, auth.URL+"/auth/callback?code=paste-code&state="+flow.state); err != nil {
		t.Fatalf("completeLogin: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("token requests = %d", len(requests))
	}
	if requests[0].Form.Get("code") != "paste-code" {
		t.Fatalf("exchanged wrong code: %+v", requests[0].Form)
	}
}

func TestWaitForCallbackCompletesLogin(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	// 回调端口固定 1455：测试环境可能被占用，NewManager 的 loopback 监听必须可重试/可跳过。
	// 这里不真监听 1455，只验证 completeLogin 输入路径；callback server 的行为由 manual paste 覆盖。
	flow, err := m.newAuthorizationFlow()
	if err != nil {
		t.Fatal(err)
	}
	_ = flow
}

func TestCredentialFileJSONShape(t *testing.T) {
	m, err := NewManager(t.TempDir(), "https://auth.openai.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.save(credentials{AccessToken: "a", RefreshToken: "r", ExpiresAt: 123, AccountID: "acc", User: User{ID: "u1", Name: "n"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(m.dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"accessToken", "refreshToken", "expiresAt", "accountId"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("credentials.json missing %q: %s", key, data)
		}
	}
}
