// Package codex manages OpenAI Codex (ChatGPT) account login and credentials.
// Login reuses the ChatGPT subscription quota; the flow mirrors the Codex CLI
// OAuth app (PKCE + loopback callback), not an EasyAgent-registered client.
package codex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ClientID is the Codex CLI's public OAuth client. It is not a secret: PKCE
// protects the exchange and the client ships the same value.
const ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

const (
	defaultAuthBaseURL = "https://auth.openai.com"
	redirectURI        = "http://localhost:1455/auth/callback"
	scope              = "openid profile email offline_access"
	jwtClaimPath       = "https://api.openai.com/auth"
	httpTimeout        = 20 * time.Second
)

// User identifies the logged-in account.
type User struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// Model describes a selectable Codex model (ChatGPT backend catalog).
type Model struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"context_window,omitempty"`
}

type credentials struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    int64  `json:"expiresAt"`
	AccountID    string `json:"accountId"`
	User         User   `json:"user,omitempty"`
}

// Manager stores credentials under <home>/codex/credentials.json (0600) and
// serializes refresh across processes.
type Manager struct {
	mu     sync.Mutex
	base   string
	dir    string
	client *http.Client
}

func NewManager(home, base string) (*Manager, error) {
	if base == "" {
		base = defaultAuthBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("Codex 认证服务地址无效")
	}
	ip := net.ParseIP(u.Hostname())
	local := strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, errors.New("Codex 认证服务必须使用 HTTPS")
	}
	if strings.TrimSpace(home) == "" {
		return nil, errors.New("无法定位 EasyAgent 用户目录")
	}
	return &Manager{
		mu:     sync.Mutex{},
		base:   strings.TrimRight(u.String(), "/"),
		dir:    filepath.Join(home, "codex"),
		client: &http.Client{Timeout: httpTimeout},
	}, nil
}

// tokenError marks refresh-token failures that require re-login.
type tokenError struct{ relogin bool }

func (e *tokenError) Error() string {
	if e.relogin {
		return "Codex 登录已失效，请重新登录"
	}
	return "Codex 登录续期失败，请检查网络后重试"
}

// ErrLoginRequired is returned when no valid credential exists.
var ErrLoginRequired = errors.New("尚未登录 Codex 账号")

func accountIDFromIDToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("id_token 不是有效的 JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("id_token 载荷无法解码")
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return "", errors.New("id_token 载荷无法解析")
	}
	auth, ok := claims[jwtClaimPath].(map[string]any)
	if !ok {
		return "", errors.New("id_token 缺少 chatgpt 账户信息")
	}
	id, _ := auth["chatgpt_account_id"].(string)
	if id == "" {
		return "", errors.New("id_token 缺少 chatgpt_account_id")
	}
	return id, nil
}

// authorizationFlow carries the PKCE verifier plus state for one login attempt.
type authorizationFlow struct {
	verifier string
	state    string
	url      string
}

func randomB64(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.New("无法生成随机数")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (m *Manager) newAuthorizationFlow() (*authorizationFlow, error) {
	verifier, err := randomB64(48)
	if err != nil {
		return nil, err
	}
	state, err := randomB64(16)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	u := m.base + "/oauth/authorize"
	q := url.Values{
		"response_type":             {"code"},
		"client_id":                 {ClientID},
		"redirect_uri":              {redirectURI},
		"scope":                     {scope},
		"code_challenge":            {challenge},
		"code_challenge_method":     {"S256"},
		"state":                     {state},
		"codex_cli_simplified_flow": {"true"},
		"originator":                {"easyagent"},
	}
	return &authorizationFlow{verifier: verifier, state: state, url: u + "?" + q.Encode()}, nil
}

// parseAuthorizationInput accepts a pasted redirect URL, "code#state",
// "code=..&state=.." or a bare code.
func parseAuthorizationInput(input string) (code, state string, err error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", "", errors.New("未提供授权码")
	}
	if u, perr := url.Parse(value); perr == nil && u.Scheme != "" && u.Host != "" {
		return u.Query().Get("code"), u.Query().Get("state"), nil
	}
	if i := strings.Index(value, "#"); i >= 0 {
		return value[:i], value[i+1:], nil
	}
	if strings.Contains(value, "code=") {
		if v, perr := url.ParseQuery(value); perr == nil {
			return v.Get("code"), v.Get("state"), nil
		}
	}
	return value, "", nil
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	IDToken          string `json:"id_token"`
	AccountID        string `json:"chatgpt_account_id"`
	ErrorMessage     string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (m *Manager) tokenRequest(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("Codex 认证请求地址无效")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, errors.New("无法连接 Codex 认证服务")
	}
	defer resp.Body.Close()
	var data tokenResponse
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data) != nil {
		return nil, fmt.Errorf("Codex 认证服务返回 HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || data.AccessToken == "" {
		relogin := resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized ||
			data.ErrorMessage == "invalid_grant"
		return nil, &tokenError{relogin: relogin}
	}
	return &data, nil
}

func credentialsFromToken(data *tokenResponse) (credentials, error) {
	accountID := data.AccountID
	if accountID == "" && data.IDToken != "" {
		id, err := accountIDFromIDToken(data.IDToken)
		if err != nil {
			return credentials{}, err
		}
		accountID = id
	}
	if accountID == "" {
		return credentials{}, errors.New("无法确定 chatgpt_account_id")
	}
	expiresIn := data.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return credentials{
		AccessToken:  data.AccessToken,
		RefreshToken: data.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second).UnixMilli(),
		AccountID:    accountID,
	}, nil
}

func (m *Manager) exchangeAuthorizationCode(ctx context.Context, code, verifier, redirect string) (credentials, error) {
	data, err := m.tokenRequest(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirect},
	})
	if err != nil {
		return credentials{}, err
	}
	cred, err := credentialsFromToken(data)
	if err != nil {
		return credentials{}, err
	}
	if err := m.save(cred); err != nil {
		return credentials{}, err
	}
	return cred, nil
}

func (m *Manager) refreshLocked(ctx context.Context, refreshToken string) (credentials, error) {
	data, err := m.tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {ClientID},
	})
	if err != nil {
		return credentials{}, err
	}
	cred, err := credentialsFromToken(data)
	if err != nil {
		return credentials{}, err
	}
	if cred.RefreshToken == "" {
		cred.RefreshToken = refreshToken
	}
	if err := m.writeLocked(cred); err != nil {
		return credentials{}, err
	}
	return cred, nil
}

// Refresh forces a token refresh using the stored refresh token.
func (m *Manager) Refresh(ctx context.Context) (credentials, error) {
	var cred credentials
	err := m.locked(ctx, func() error {
		data, err := m.readLocked()
		if err != nil {
			return err
		}
		if data.RefreshToken == "" {
			return ErrLoginRequired
		}
		cred, err = m.refreshLocked(ctx, data.RefreshToken)
		return err
	})
	return cred, err
}

// completeLogin validates the pasted state and exchanges the code.
func (m *Manager) completeLogin(ctx context.Context, flow *authorizationFlow, input string) error {
	code, state, err := parseAuthorizationInput(input)
	if err != nil {
		return err
	}
	if code == "" {
		return errors.New("授权回调缺少 code 参数")
	}
	if state != "" && subtle.ConstantTimeCompare([]byte(state), []byte(flow.state)) != 1 {
		return errors.New("授权回调 state 不匹配")
	}
	_, err = m.exchangeAuthorizationCode(ctx, code, flow.verifier, redirectURI)
	return err
}

func (m *Manager) locked(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	// Unix 上校验目录仅当前用户可访问；Windows 的 POSIX 权限位无意义，跳过。
	if runtime.GOOS != "windows" {
		info, err := os.Lstat(m.dir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("Codex 凭据目录必须仅当前用户可访问")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func (m *Manager) readLocked() (credentials, error) {
	var data credentials
	path := filepath.Join(m.dir, "credentials.json")
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return data, ErrLoginRequired
	}
	if err != nil {
		return data, errors.New("Codex 凭据文件不可读取")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return data, errors.New("Codex 凭据文件不可安全读取")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return data, errors.New("Codex 凭据文件不可安全读取")
	}
	if json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(&data) != nil || data.AccessToken == "" {
		return credentials{}, errors.New("Codex 登录凭据无效，请重新登录")
	}
	return data, nil
}

func (m *Manager) writeLocked(data credentials) error {
	if data.AccessToken == "" {
		return errors.New("服务未返回有效的 Codex 登录凭据")
	}
	encoded, err := json.Marshal(data) // #nosec G117 -- credentials are deliberately written only to a 0600 private file.
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.dir, ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(encoded)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("无法保存 Codex 登录凭据")
	}
	if err := os.Rename(f.Name(), filepath.Join(m.dir, "credentials.json")); err != nil {
		return errors.New("无法保存 Codex 登录凭据")
	}
	return nil
}

func (m *Manager) save(cred credentials) error {
	return m.locked(context.Background(), func() error { return m.writeLocked(cred) })
}

// Credential is the exported view of the stored login.
type Credential struct {
	AccessToken string
	AccountID   string
	User        User
}

// CurrentCredentials returns a valid credential, refreshing an expired access
// token first. ErrLoginRequired when not logged in.
func (m *Manager) CurrentCredentials(ctx context.Context) (Credential, error) {
	if _, err := m.AccessToken(ctx); err != nil {
		return Credential{}, err
	}
	var cred Credential
	err := m.locked(ctx, func() error {
		data, err := m.readLocked()
		if err != nil {
			return err
		}
		cred = Credential{AccessToken: data.AccessToken, AccountID: data.AccountID, User: data.User}
		return nil
	})
	return cred, err
}

// Status reports the logged-in account, if any.
func (m *Manager) Status(ctx context.Context) (User, bool, error) {
	var data credentials
	err := m.locked(ctx, func() error {
		var err error
		data, err = m.readLocked()
		return err
	})
	if errors.Is(err, ErrLoginRequired) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	if data.ExpiresAt <= time.Now().UnixMilli() && data.RefreshToken == "" {
		return data.User, false, nil
	}
	return data.User, true, nil
}

func (m *Manager) Logout(ctx context.Context) error {
	return m.locked(ctx, func() error {
		err := os.Remove(filepath.Join(m.dir, "credentials.json"))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	})
}

// AccessToken returns a valid access token, refreshing when near expiry.
func (m *Manager) AccessToken(ctx context.Context) (string, error) { return m.accessToken(ctx, "") }

// RefreshAfterRejection refreshes after the server rejected the given token.
// If another request already refreshed it, the newer token is reused.
func (m *Manager) RefreshAfterRejection(ctx context.Context, rejected string) (string, error) {
	return m.accessToken(ctx, rejected)
}

func (m *Manager) accessToken(ctx context.Context, rejected string) (string, error) {
	var result string
	err := m.locked(ctx, func() error {
		data, err := m.readLocked()
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		if rejected != "" && data.AccessToken != rejected && data.ExpiresAt > now {
			result = data.AccessToken
			return nil
		}
		if rejected == "" && data.ExpiresAt > now+60_000 {
			result = data.AccessToken
			return nil
		}
		if data.RefreshToken == "" {
			if rejected == "" && data.ExpiresAt > now {
				result = data.AccessToken
				return nil
			}
			return ErrLoginRequired
		}
		cred, err := m.refreshLocked(ctx, data.RefreshToken)
		if err != nil {
			var te *tokenError
			if errors.As(err, &te) && !te.relogin && rejected == "" && data.ExpiresAt > now {
				result = data.AccessToken
				return nil
			}
			return err
		}
		result = cred.AccessToken
		return nil
	})
	return result, err
}
