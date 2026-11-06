// Package easycode manages a user's own EasyCode login and model catalog.
package easycode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api-code.deepvlab.ai"
const userAgent = "EasyAgent"

var ErrLoginRequired = errors.New("EasyCode 登录已失效，请重新使用飞书登录")

type User struct {
	ID    string `json:"userId,omitempty"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type credentials struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    int64  `json:"expiresAt"`
	User         User   `json:"user,omitempty"`
}

type Model struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ContextWindow  int    `json:"context_window,omitempty"`
	SupportsImages *bool  `json:"supports_images,omitempty"`
}

// Manager keeps credentials isolated from the EasyCode client's own session.
// The file is private to the OS user and refresh is serialized across processes.
type Manager struct {
	base   string
	dir    string
	client *http.Client
	ports  []int
}

func NewManager(home, base string) (*Manager, error) {
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("EasyCode 服务地址无效")
	}
	ip := net.ParseIP(u.Hostname())
	local := strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, errors.New("EasyCode 服务必须使用 HTTPS")
	}
	if strings.TrimSpace(home) == "" {
		return nil, errors.New("无法定位 EasyAgent 用户目录")
	}
	return &Manager{
		base: strings.TrimRight(u.String(), "/"), dir: filepath.Join(home, "easycode"),
		client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		ports:  []int{7863, 7864, 7865, 7866, 7867, 7868, 7869, 7870, 7871, 7872, 7873},
	}, nil
}

func (m *Manager) BaseURL() string { return m.base }

func (m *Manager) locked(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(m.dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("EasyCode 凭据目录必须仅当前用户可访问")
	}
	lock, err := lockCredentials(ctx, filepath.Join(m.dir, ".lock"))
	if err != nil {
		return err
	}
	defer lock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func (m *Manager) readLocked() (credentials, error) {
	var data credentials
	path := filepath.Join(m.dir, "credentials.json")
	file, err := openCredential(path)
	if os.IsNotExist(err) {
		return data, ErrLoginRequired
	}
	if err != nil {
		return data, errors.New("EasyCode 凭据文件不可安全读取")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return data, errors.New("EasyCode 凭据文件不可安全读取")
	}
	if json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(&data) != nil || data.AccessToken == "" {
		return credentials{}, errors.New("EasyCode 登录凭据无效，请重新登录")
	}
	return data, nil
}

func (m *Manager) writeLocked(data credentials) error {
	if data.AccessToken == "" {
		return errors.New("服务未返回有效的 EasyCode 登录凭据")
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
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(encoded)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("无法保存 EasyCode 登录凭据")
	}
	if err := os.Rename(f.Name(), filepath.Join(m.dir, "credentials.json")); err != nil {
		return errors.New("无法保存 EasyCode 登录凭据")
	}
	return nil
}

func (m *Manager) save(ctx context.Context, data credentials) error {
	return m.locked(ctx, func() error { return m.writeLocked(data) })
}

func (m *Manager) load(ctx context.Context) (credentials, error) {
	var data credentials
	err := m.locked(ctx, func() error { var err error; data, err = m.readLocked(); return err })
	return data, err
}

func (m *Manager) Status(ctx context.Context) (User, bool, error) {
	data, err := m.load(ctx)
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
		for _, name := range []string{"credentials.json", cliSelectionFile} {
			if err := os.Remove(filepath.Join(m.dir, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	})
}

func (m *Manager) AccessToken(ctx context.Context) (string, error) { return m.accessToken(ctx, "") }

// RefreshAfterRejection exchanges an access token rejected by the server. If
// another request already refreshed it, it uses the newer token instead.
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
		var response struct {
			Success bool `json:"success"`
			Data    struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				ExpiresIn    int64  `json:"expiresIn"`
			} `json:"data"`
		}
		status, err := m.jsonRequest(ctx, http.MethodPost, "/auth/jwt/refresh", "", map[string]string{"refreshToken": data.RefreshToken}, &response)
		if err != nil || !response.Success || response.Data.AccessToken == "" {
			if status == http.StatusUnauthorized {
				return ErrLoginRequired
			}
			if rejected == "" && data.ExpiresAt > now {
				result = data.AccessToken
				return nil
			}
			return errors.New("EasyCode 登录续期失败，请检查网络后重试")
		}
		data.AccessToken = response.Data.AccessToken
		if response.Data.RefreshToken != "" {
			data.RefreshToken = response.Data.RefreshToken
		}
		lifetime := response.Data.ExpiresIn
		if lifetime <= 0 {
			lifetime = 900
		}
		data.ExpiresAt = time.Now().Add(time.Duration(lifetime) * time.Second).UnixMilli()
		if err := m.writeLocked(data); err != nil {
			return err
		}
		result = data.AccessToken
		return nil
	})
	return result, err
}

// jsonRequest deliberately does not expose response bodies in errors: the
// authentication endpoints may return credentials or sensitive account data.
func (m *Manager) jsonRequest(ctx context.Context, method, path, token string, body, out any) (int, error) {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, payload)
	if err != nil {
		return 0, errors.New("EasyCode 请求地址无效")
	}
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, errors.New("无法连接 EasyCode 服务")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("EasyCode 服务返回 HTTP %d", resp.StatusCode)
	}
	if out != nil && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out) != nil {
		return resp.StatusCode, errors.New("EasyCode 服务返回的数据无效")
	}
	return resp.StatusCode, nil
}

func (m *Manager) Models(ctx context.Context) ([]Model, error) {
	token, err := m.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	var response struct {
		Success bool `json:"success"`
		Data    []struct {
			Name           string `json:"name"`
			DisplayName    string `json:"displayName"`
			MaxToken       int    `json:"maxToken"`
			Available      *bool  `json:"available"`
			SupportsImages *bool  `json:"supportsImages"`
		} `json:"data"`
	}
	status, err := m.jsonRequest(ctx, http.MethodGet, "/web-api/models", token, nil, &response)
	if status == http.StatusUnauthorized {
		token, err = m.RefreshAfterRejection(ctx, token)
		if err == nil {
			_, err = m.jsonRequest(ctx, http.MethodGet, "/web-api/models", token, nil, &response)
		}
	}
	if err != nil {
		return nil, err
	}
	if !response.Success || response.Data == nil {
		return nil, errors.New("EasyCode 模型目录无效")
	}
	result := make([]Model, 0, len(response.Data))
	for _, item := range response.Data {
		id := strings.TrimSpace(item.Name)
		if id == "" || len(id) > 200 || strings.ContainsAny(id, "\r\n\t") || item.Available != nil && !*item.Available {
			continue
		}
		name := strings.TrimSpace(item.DisplayName)
		if name == "" {
			name = id
		}
		window := item.MaxToken
		if window < 0 {
			window = 0
		}
		if window > 160000 {
			window = 160000
		}
		result = append(result, Model{ID: id, Name: name, ContextWindow: window, SupportsImages: item.SupportsImages})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
