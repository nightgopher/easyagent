package codex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLoginSessionWaitCompletes 通过真实 loopback 回调完成登录。
func TestLoginSessionWaitCompletes(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	session, err := m.StartLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.portBusy {
		t.Skip("callback port 1455 busy")
	}

	// 模拟浏览器重定向到 loopback 回调
	cb, err := url.Parse(redirectURI + "?code=cb-code&state=" + session.flow.state)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(cb.String())
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", resp.StatusCode)
	}

	if err := session.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 || requests[0].Form.Get("code") != "cb-code" {
		t.Fatalf("token requests = %+v", requests)
	}
	if _, loggedIn, _ := m.Status(context.Background()); !loggedIn {
		t.Fatal("not logged in after callback")
	}
}

func TestLoginCallbackDoesNotReportSuccessWhenTokenExchangeFails(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "synthetic-sensitive-error", http.StatusUnauthorized)
	}))
	defer auth.Close()
	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	session, err := m.StartLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.portBusy {
		t.Skip("callback port 1455 busy")
	}
	resp, err := http.Get(redirectURI + "?code=bad&state=" + session.flow.state)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK || strings.Contains(string(body), "synthetic-sensitive-error") {
		t.Fatalf("incorrect callback response: %d %s", resp.StatusCode, body)
	}
	if err := session.Wait(context.Background()); err == nil {
		t.Fatal("failed token exchange accepted")
	}
}

// TestLoginSessionWrongStateRejected state 不匹配时必须拒绝。
func TestLoginSessionWrongStateRejected(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	session, err := m.StartLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.portBusy {
		t.Skip("callback port 1455 busy")
	}

	cb, _ := url.Parse(redirectURI + "?code=evil&state=wrong-state")
	resp, err := http.Get(cb.String())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal("wrong state accepted", resp.StatusCode)
	}
	// A bogus callback must not consume the one-shot login session.
	good, err := http.Get(redirectURI + "?code=good&state=" + session.flow.state)
	if err != nil {
		t.Fatal(err)
	}
	good.Body.Close()
	if good.StatusCode != http.StatusOK {
		t.Fatalf("valid callback status %d", good.StatusCode)
	}
	if err := session.Wait(context.Background()); err != nil {
		t.Fatalf("valid callback failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 || requests[0].Form.Get("code") != "good" {
		t.Fatal("unexpected token exchange after wrong state")
	}
}

// TestLoginSessionMissingStateRejected state 缺失同样必须拒绝：
// 授权请求总是携带 state，回调缺失 state 即不可信，不得进入 token 交换。
func TestLoginSessionMissingStateRejected(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	session, err := m.StartLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.portBusy {
		t.Skip("callback port 1455 busy")
	}

	cb, _ := url.Parse(redirectURI + "?code=cb-code")
	resp, err := http.Get(cb.String())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatal("missing state accepted", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 0 {
		t.Fatal("token exchange reached with missing state")
	}
}

// TestCompleteManualPasteURL 手动粘贴回调 URL 路径。
func TestCompleteManualPasteURL(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	session, err := m.StartLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	paste := auth.URL + "/auth/callback?code=pasted&state=" + session.flow.state
	if err := session.CompleteManual(context.Background(), paste); err != nil {
		t.Fatalf("CompleteManual: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 || requests[0].Form.Get("code") != "pasted" {
		t.Fatalf("token requests = %+v", requests)
	}
}

// TestStartLoginAuthorizeURL 形状校验。
func TestStartLoginAuthorizeURL(t *testing.T) {
	m, err := NewManager(t.TempDir(), "https://auth.openai.com")
	if err != nil {
		t.Fatal(err)
	}
	session, err := m.StartLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	u, err := url.Parse(session.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "auth.openai.com" || u.Path != "/oauth/authorize" {
		t.Fatalf("authorize url = %s", session.URL)
	}
	if !strings.Contains(session.URL, "code_challenge_method=S256") {
		t.Fatal("PKCE challenge missing")
	}
}

var _ = fmt.Sprintf
var _ = time.Now

// TestLoginSessionDoubleCallback：浏览器可能对同一回调重定向两次（用户刷新、
// 代理重试）。第二次回调不得让 close(session.closed) panic，也不得重复交换
// 令牌导致 Wait 结果错乱。
func TestLoginSessionDoubleCallback(t *testing.T) {
	var mu sync.Mutex
	var requests []tokenRequest
	auth := tokenServer(t, &requests, &mu)
	defer auth.Close()

	m, err := NewManager(t.TempDir(), auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	session, err := m.StartLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.portBusy {
		t.Skip("callback port 1455 busy")
	}

	cb, err := url.Parse(redirectURI + "?code=cb-code&state=" + session.flow.state)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			resp, err := http.Get(cb.String())
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	<-done
	<-done
	if err := session.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	session.Close()
	session.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("token exchange must happen exactly once: %d", len(requests))
	}
}
