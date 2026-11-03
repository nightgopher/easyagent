package easycode

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type tenant struct {
	AppID string `json:"appId"`
	Label string `json:"label"`
}

type loginResult struct {
	user User
	err  error
}

// LoginSession owns a single loopback callback listener. URL is safe to open
// in the user's browser; credentials are never returned in its response.
type LoginSession struct {
	URL      string
	manager  *Manager
	server   *http.Server
	listener net.Listener
	callback string
	nonce    string
	tenants  []tenant
	done     chan loginResult
	mu       sync.Mutex
	claimed  bool
}

var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>EasyAgent 飞书登录</title></head><body><main><h1>登录 EasyCode</h1><p>选择飞书组织后，将前往飞书完成授权。模型请求使用所登录账号的额度。</p>{{range $i, $t := .Tenants}}<p><a href="/start?state={{$.State}}&amp;tenant={{$i}}">{{if $t.Label}}{{$t.Label}}{{else}}飞书登录{{end}}</a></p>{{end}}</main></body></html>`))

func (m *Manager) StartLogin(ctx context.Context) (*LoginSession, error) {
	var config struct {
		Success bool `json:"success"`
		Data    struct {
			Feishu  tenant   `json:"feishu"`
			Tenants []tenant `json:"feishuTenants"`
		} `json:"data"`
	}
	if _, err := m.jsonRequest(ctx, http.MethodGet, "/api/config/client", "", nil, &config); err != nil {
		return nil, err
	}
	if !config.Success {
		return nil, errors.New("EasyCode 未提供飞书登录配置")
	}
	tenants := config.Data.Tenants
	if len(tenants) == 0 {
		tenants = []tenant{config.Data.Feishu}
	}
	valid := make([]tenant, 0, len(tenants))
	seen := map[string]bool{}
	for _, t := range tenants {
		if t.AppID == "" || len(t.AppID) > 200 || strings.Trim(t.AppID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" || seen[t.AppID] {
			continue
		}
		seen[t.AppID] = true
		valid = append(valid, t)
	}
	if len(valid) == 0 {
		return nil, errors.New("EasyCode 未提供可用的飞书组织")
	}
	var nonceBytes [32]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return nil, errors.New("无法启动安全登录")
	}
	var listener net.Listener
	var err error
	for _, port := range m.ports {
		listener, err = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			break
		}
	}
	if listener == nil {
		return nil, errors.New("无法启动本机登录回调，请稍后重试")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	s := &LoginSession{manager: m, listener: listener, nonce: hex.EncodeToString(nonceBytes[:]), tenants: valid, done: make(chan loginResult, 1), callback: fmt.Sprintf("http://localhost:%d/callback", port)}
	s.URL = fmt.Sprintf("http://127.0.0.1:%d/?state=%s", port, s.nonce)
	s.server = &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second}
	go func() { _ = s.server.Serve(listener) }()
	return s, nil
}

func (s *LoginSession) Close() { _ = s.server.Close(); _ = s.listener.Close() }

func (s *LoginSession) Wait(ctx context.Context) (User, error) {
	select {
	case result := <-s.done:
		return result.user, result.err
	case <-ctx.Done():
		return User{}, ctx.Err()
	}
}

func sameState(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *LoginSession) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || (host != "localhost" && host != "127.0.0.1") || port != strconv.Itoa(s.listener.Addr().(*net.TCPAddr).Port) || r.Method != http.MethodGet {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	switch r.URL.Path {
	case "/":
		if !sameState(q.Get("state"), s.nonce) {
			http.Error(w, "Invalid login request", 400)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = loginPage.Execute(w, struct {
			State   string
			Tenants []tenant
		}{s.nonce, s.tenants})
	case "/start":
		index, err := strconv.Atoi(q.Get("tenant"))
		if !sameState(q.Get("state"), s.nonce) || err != nil || index < 0 || index >= len(s.tenants) {
			http.Error(w, "Invalid login request", 400)
			return
		}
		params := url.Values{"app_id": {s.tenants[index].AppID}, "redirect_uri": {s.callback}, "response_type": {"code"}, "scope": {"contact:user.employee_id:readonly"}, "state": {s.nonce + "." + strconv.Itoa(index)}}
		http.Redirect(w, r, "https://open.feishu.cn/open-apis/authen/v1/authorize?"+params.Encode(), http.StatusFound)
	case "/callback":
		state := q.Get("state")
		idx := strings.LastIndexByte(state, '.')
		if idx < 0 || !sameState(state[:idx], s.nonce) {
			http.Error(w, "Invalid authorization state", 400)
			return
		}
		index, err := strconv.Atoi(state[idx+1:])
		if err != nil || index < 0 || index >= len(s.tenants) || state[idx+1:] != strconv.Itoa(index) {
			http.Error(w, "Invalid authorization state", 400)
			return
		}
		s.mu.Lock()
		if s.claimed {
			s.mu.Unlock()
			http.Error(w, "Authorization already received", http.StatusConflict)
			return
		}
		s.claimed = true
		s.mu.Unlock()
		var result loginResult
		if q.Get("error") != "" || q.Get("code") == "" {
			result.err = errors.New("飞书授权未完成，请重新登录")
		} else {
			result.user, result.err = s.exchange(r.Context(), q.Get("code"), s.tenants[index].AppID)
		}
		if result.err != nil {
			http.Error(w, "登录未完成，请返回 EasyAgent 重试。", http.StatusBadGateway)
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = fmt.Fprint(w, "登录成功，可以关闭此页面并返回 EasyAgent。")
		}
		s.done <- result
	default:
		http.NotFound(w, r)
	}
}

func (s *LoginSession) exchange(ctx context.Context, code, appID string) (User, error) {
	if len(code) > 4096 || strings.ContainsAny(code, "\r\n\x00") {
		return User{}, errors.New("飞书授权码无效")
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			AccessToken string `json:"accessToken"`
		} `json:"data"`
	}
	_, err := s.manager.jsonRequest(ctx, http.MethodPost, "/api/auth/feishu/exchange", "", map[string]string{"code": code, "redirect_uri": s.callback, "app_id": appID}, &response)
	if err != nil || !response.Success || response.Data.AccessToken == "" {
		return User{}, errors.New("飞书授权交换失败，请重新登录")
	}
	var jwt struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		User         User   `json:"user"`
	}
	_, err = s.manager.jsonRequest(ctx, http.MethodPost, "/auth/jwt/feishu-login", "", map[string]any{"feishuAccessToken": response.Data.AccessToken, "clientInfo": map[string]any{"platform": "easyagent", "userAgent": userAgent, "timestamp": time.Now().UnixMilli()}}, &jwt)
	if err != nil || jwt.AccessToken == "" {
		return User{}, errors.New("EasyCode 登录失败，请检查账号授权后重试")
	}
	lifetime := jwt.ExpiresIn
	if lifetime <= 0 {
		lifetime = 900
	}
	data := credentials{AccessToken: jwt.AccessToken, RefreshToken: jwt.RefreshToken, ExpiresAt: time.Now().Add(time.Duration(lifetime) * time.Second).UnixMilli(), User: jwt.User}
	if err := s.manager.save(ctx, data); err != nil {
		return User{}, err
	}
	return jwt.User, nil
}
