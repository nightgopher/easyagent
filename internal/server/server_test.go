package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/nightgopher/easyagent/internal/app"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestApp(t *testing.T) *app.App {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Provider = "openai"
	cfg.OpenAIAPIKey = "test-key"
	cfg.OpenAIBaseURL = "http://localhost:4001"
	application, err := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { application.Close() })
	return application
}

func TestServer_Health(t *testing.T) {
	application := newTestApp(t)
	srv := New(application, nil)

	req := localReq(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	srv.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "ok", resp["status"])
}

func TestSessionInfoReportsPathPolicyBehindAuthentication(t *testing.T) {
	for _, allow := range []bool{false, true} {
		cfg := config.Default()
		cfg.DataDir = t.TempDir()
		cfg.Provider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "test-key", "http://127.0.0.1:1"
		cfg.AllowOutsideWorkspace = allow
		application, err := app.New(app.AppOptions{Config: cfg})
		require.NoError(t, err)
		t.Cleanup(func() { application.Close() })
		srv := New(application, nil)
		srv.SetAPIKey("secret-key")
		req := localReq(http.MethodPost, "/sessions", nil)
		req.Header.Set("Authorization", "Bearer secret-key")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		var session SessionResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&session))
		path := "/sessions/" + session.ID + "/info"
		req = localReq(http.MethodGet, path, nil)
		w = httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code)
		req.Header.Set("Authorization", "Bearer secret-key")
		w = httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		var info map[string]any
		require.NoError(t, json.NewDecoder(w.Body).Decode(&info))
		assert.Equal(t, allow, info["allow_outside_workspace"])
		wantHome, _ := os.UserHomeDir()
		assert.Equal(t, wantHome, info["home"], "clients expand ~/ against the host's home")
	}
}

func TestChatStreamRouteIsAuthenticated(t *testing.T) {
	srv := New(newTestApp(t), nil)
	srv.SetAPIKey("test-key")
	for _, key := range []string{"", "test-key"} {
		req := httptest.NewRequest(http.MethodPost, "/chat/stream", bytes.NewBufferString(`{"prompt":""}`))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		want := http.StatusUnauthorized
		if key != "" {
			want = http.StatusBadRequest
		}
		assert.Equal(t, want, w.Code, w.Body.String())
	}
}

func TestLoopbackListen(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080":    true,
		"[::1]:8080":        true,
		"localhost:8080":    true,
		":8080":             false,
		"0.0.0.0:8080":      false,
		"[::]:8080":         false,
		"192.168.5.16:8080": false,
		"agent.lan:8080":    false,
	} {
		assert.Equal(t, want, loopbackListen(addr), addr)
	}
}
