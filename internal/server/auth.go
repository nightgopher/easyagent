package server

import (
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/nightgopher/easyagent/sdk/config"
)

// 访问控制模型（默认安全，本地零配置可用）：
//   1. 配置了 API key（EA_API_KEY）→ 所有请求必须带 Bearer token（/health 除外）；
//   2. 未配置且 EA_ALLOW_NO_AUTH=1 → 完全开放（旧行为，仅建议本机调试）；
//   3. 未配置（默认）→ 仅放行 loopback 请求；外部访问返回 401，
//      本机消费方（网页 UI、飞书 bridge、桌面端）零配置继续可用。

func (s *Server) authorized(r *http.Request) bool {
	if s.apiKey != "" {
		return s.checkToken(r, bearerToken(r))
	}
	return s.openAccessAllowed(r)
}

// checkToken 以常量时间比较令牌，并对错误令牌按来源 IP 限流。
// 封禁期内即使令牌正确也拒绝；空令牌（未登录的探测）不计入失败。
func (s *Server) checkToken(r *http.Request, token string) bool {
	ip := remoteIP(r)
	if s.authFails.blockedFor(ip) > 0 {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.apiKey)) == 1 {
		s.authFails.reset(ip)
		return true
	}
	if token != "" {
		blocked := s.authFails.fail(ip)
		slog.Warn("auth failed", "remote", r.RemoteAddr, "path", r.URL.Path, "blocked", blocked)
	}
	return false
}

// denyStatus 返回认证失败时应使用的状态码：封禁期内为 429，否则 401。
func (s *Server) denyStatus(w http.ResponseWriter, r *http.Request) int {
	if d := s.authFails.blockedFor(remoteIP(r)); d > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(d.Seconds())+1))
		return http.StatusTooManyRequests
	}
	return http.StatusUnauthorized
}

// wsAuthorized 在 WebSocket 升级前做同等校验；token 支持 ?token= 查询参数
// （浏览器 WebSocket 无法自定义 header）。
func (s *Server) wsAuthorized(r *http.Request) bool {
	if s.apiKey != "" {
		token := r.URL.Query().Get("token")
		if token == "" {
			token = bearerToken(r)
		}
		return s.checkToken(r, token)
	}
	return s.openAccessAllowed(r)
}

// openAccessAllowed：显式开放 或 来源为 loopback。
func (s *Server) openAccessAllowed(r *http.Request) bool {
	if s.allowNoAuth {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
		return after
	}
	return auth
}

// ─── 路径收窄 ────────────────────────────────────────────────────────────────

// securePath 把用户提供的路径限制在 root 之内，返回可安全使用的绝对路径。
// 处理三件事：绝对化与清洗、../ 穿越、符号链接逃逸（已存在部分按真实路径解析）。
func securePath(root, target string) (string, error) {
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		realRoot = absRoot
	}

	var absTarget string
	if filepath.IsAbs(target) {
		absTarget = filepath.Clean(target)
	} else {
		absTarget = filepath.Join(realRoot, target)
	}

	resolved := resolveExistingSymlinks(absTarget)
	rel, err := filepath.Rel(realRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", target)
	}
	return resolved, nil
}

// resolveExistingSymlinks 解析路径中已存在部分的符号链接；
// 末端不存在时向上找到最深存在的祖先再拼回。
func resolveExistingSymlinks(p string) string {
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		return rp
	}
	dir, base := filepath.Split(p)
	cleanDir := filepath.Clean(dir)
	if rd, err := filepath.EvalSymlinks(cleanDir); err == nil {
		return filepath.Join(rd, base)
	}
	// 多级不存在的目录：逐级向上解析
	parent := cleanDir
	for parent != filepath.Dir(parent) {
		if rp, err := filepath.EvalSymlinks(parent); err == nil {
			relToParent, relErr := filepath.Rel(parent, p)
			if relErr == nil {
				return filepath.Join(rp, relToParent)
			}
		}
		parent = filepath.Dir(parent)
	}
	return p
}

// env 配置在构造期读取；与 config 包风格一致（服务级开关不进 per-session 配置）。
func envAllowNoAuth() bool {
	return config.Env("EA_ALLOW_NO_AUTH") == "1"
}

func envAllowedOrigins() []string {
	v := config.Env("EA_ALLOWED_ORIGINS")
	if v == "" {
		return nil
	}
	var origins []string
	for _, o := range strings.Split(v, ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	return origins
}
