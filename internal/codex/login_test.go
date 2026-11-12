package codex

import (
	"context"
	"fmt"
	"net/http"
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
	if err := session.Wait(context.Background()); err == nil {
		t.Fatal("wrong state accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 0 {
		t.Fatal("token exchange reached with wrong state")
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
