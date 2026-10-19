package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func authReq(remote, token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	r.RemoteAddr = remote
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func serveAuth(s *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, r)
	return w
}

func TestAuthLimiter_BlocksAfterRepeatedFailures(t *testing.T) {
	s := &Server{apiKey: "secret"}
	for i := 0; i < authFailLimit-1; i++ {
		assert.Equal(t, http.StatusUnauthorized, serveAuth(s, authReq("203.0.113.9:1234", "wrong")).Code)
	}
	// 第 authFailLimit 次错误触发封禁，并在同一响应中返回 429。
	w := serveAuth(s, authReq("203.0.113.9:1234", "wrong"))
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.NotEmpty(t, w.Header().Get("Retry-After"))

	// 封禁期内即使令牌正确也拒绝；其他来源不受影响。
	assert.Equal(t, http.StatusTooManyRequests, serveAuth(s, authReq("203.0.113.9:1234", "secret")).Code)
	assert.Equal(t, http.StatusOK, serveAuth(s, authReq("203.0.113.10:1234", "secret")).Code)
}

func TestAuthLimiter_BlockExpires(t *testing.T) {
	now := time.Now()
	s := &Server{apiKey: "secret"}
	s.authFails.now = func() time.Time { return now }
	for i := 0; i < authFailLimit; i++ {
		serveAuth(s, authReq("203.0.113.9:1", "wrong"))
	}
	assert.Equal(t, http.StatusTooManyRequests, serveAuth(s, authReq("203.0.113.9:1", "secret")).Code)

	now = now.Add(authBlockTime + time.Second)
	assert.Equal(t, http.StatusOK, serveAuth(s, authReq("203.0.113.9:1", "secret")).Code)
}

func TestAuthLimiter_SuccessResetsCountAndMissingTokenNotCounted(t *testing.T) {
	s := &Server{apiKey: "secret"}
	for i := 0; i < authFailLimit-1; i++ {
		serveAuth(s, authReq("203.0.113.9:1", "wrong"))
	}
	assert.Equal(t, http.StatusOK, serveAuth(s, authReq("203.0.113.9:1", "secret")).Code)
	for i := 0; i < authFailLimit*2; i++ {
		assert.Equal(t, http.StatusUnauthorized, serveAuth(s, authReq("203.0.113.9:1", "")).Code)
	}
	assert.Equal(t, http.StatusOK, serveAuth(s, authReq("203.0.113.9:1", "secret")).Code)
}

func TestAuthLimiter_LoopbackExempt(t *testing.T) {
	s := &Server{apiKey: "secret"}
	for i := 0; i < authFailLimit*2; i++ {
		assert.Equal(t, http.StatusUnauthorized, serveAuth(s, authReq("127.0.0.1:5555", "wrong")).Code)
	}
	assert.Equal(t, http.StatusOK, serveAuth(s, authReq("127.0.0.1:5555", "secret")).Code)
}

func TestWSAuthorized_QueryTokenThrottled(t *testing.T) {
	s := &Server{apiKey: "secret"}
	for i := 0; i < authFailLimit; i++ {
		r := httptest.NewRequest(http.MethodGet, "/ws?token=wrong", nil)
		r.RemoteAddr = "203.0.113.9:1"
		assert.False(t, s.wsAuthorized(r))
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ws?token=secret", nil)
	r.RemoteAddr = "203.0.113.9:1"
	s.handleWebSocket(w, r)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}
