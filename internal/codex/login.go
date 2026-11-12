package codex

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// LoginSession owns one loopback callback listener on the Codex CLI's
// registered redirect port (1455). When the port is busy the caller falls
// back to pasting the redirect URL manually.
type LoginSession struct {
	URL      string
	portBusy bool

	manager *Manager
	flow    *authorizationFlow
	server  *http.Server
	closed  chan struct{}
	done    chan error
}

// StartLogin opens the loopback callback server and returns the authorize URL
// to open in a browser.
func (m *Manager) StartLogin(ctx context.Context) (*LoginSession, error) {
	flow, err := m.newAuthorizationFlow()
	if err != nil {
		return nil, err
	}
	u, perr := url.Parse(flow.url)
	if perr != nil {
		return nil, errors.New("codex 授权地址无效")
	}
	session := &LoginSession{
		URL:     u.String(),
		manager: m,
		flow:    flow,
		closed:  make(chan struct{}),
		done:    make(chan error, 1),
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:1455")
	if err != nil {
		// 端口被占用（另一个登录或 Codex CLI 本体）；仍可走手动粘贴路径。
		session.portBusy = true
		return session, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			http.Error(w, "授权未完成: "+e, http.StatusBadRequest)
			session.fail(errors.New("codex 授权被拒绝: " + e))
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "回调缺少 code 参数", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprint(w, "登录成功，可以关闭此页面并返回 EasyAgent。")
		go func() {
			input := redirectURI + "?" + q.Encode()
			session.done <- session.manager.completeLogin(context.WithoutCancel(ctx), session.flow, input)
			close(session.closed)
		}()
	})
	session.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if serr := session.server.Serve(listener); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			session.fail(serr)
		}
	}()
	return session, nil
}

// Wait blocks until login completes, fails, or ctx ends. When the callback
// port is busy it returns errPortBusy immediately; paste the URL manually via
// CompleteManual instead.
func (s *LoginSession) Wait(ctx context.Context) error {
	if s.portBusy {
		return errPortBusy
	}
	select {
	case err := <-s.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ErrPortBusy reports that the loopback callback port is taken; the user must
// paste the redirect URL manually.
var ErrPortBusy = errors.New("回调端口 1455 被占用，请手动粘贴授权回调地址")

var errPortBusy = ErrPortBusy

// CompleteManual exchanges a pasted redirect URL / code#state.
func (s *LoginSession) CompleteManual(ctx context.Context, input string) error {
	err := s.manager.completeLogin(ctx, s.flow, input)
	if err == nil {
		s.succeed()
	}
	return err
}

func (s *LoginSession) succeed() {
	select {
	case outcome := <-s.done:
		_ = outcome // 结果已由等待方送达，这里只负责清空挂起的投递。
	default:
		s.done <- nil
	}
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
}

func (s *LoginSession) fail(err error) {
	select {
	case prev := <-s.done:
		_ = prev // 已有结果投递在途，丢弃并保留传入的错误。
	default:
		s.done <- err
	}
}

// Close shuts the callback server down.
func (s *LoginSession) Close() {
	if s.server != nil {
		_ = s.server.Close()
	}
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
}
