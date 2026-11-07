package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/internal/app"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

func TestBuiltinToolsSettingsAffectNewSessionsAndPersist(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	s.SetAPIKey("settings-test")
	request := func(method, body, token string) *httptest.ResponseRecorder {
		r := localReq(method, "/settings/builtin-tools", strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	require.Equal(t, http.StatusUnauthorized, request("GET", "", "").Code)
	require.Equal(t, http.StatusUnauthorized, request("POST", `{"tool":"web_search","enabled":false}`, "").Code)
	for _, body := range []string{`{}`, `null`, `{"tool":"unknown","enabled":true}`, `{"tool":"web_search","enabled":"yes"}`, `{"tool":"web_search","enabled":false,"unknown":1}`, `{"tool":"web_search","enabled":true} {}`} {
		require.Equal(t, http.StatusBadRequest, request("POST", body, "settings-test").Code)
	}
	ctx := context.Background()
	previous, err := s.app.NewSession(ctx)
	require.NoError(t, err)
	require.Contains(t, previous.ToolNames(), "web_search")
	require.Contains(t, previous.ToolNames(), "read")
	for _, id := range []string{"web_search", "file_read", "file_write"} {
		w := request("POST", `{"tool":"`+id+`","enabled":false}`, "settings-test")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	require.Contains(t, previous.ToolNames(), "web_search")
	require.NoError(t, previous.RefreshTools(ctx))
	require.Contains(t, previous.ToolNames(), "web_search", "existing sessions keep their snapshot")
	require.Contains(t, previous.ToolNames(), "read")
	for _, channel := range []string{"", "feishu"} {
		var current interface{ ToolNames() []string }
		if channel == "" {
			current, err = s.app.NewSession(ctx)
		} else {
			current, err = s.resolveChannelSession(ctx, "", channel)
		}
		require.NoError(t, err)
		for _, name := range []string{"web_search", "read", "grep", "find", "ls", "write", "edit", "patch", "delete_file"} {
			require.NotContains(t, current.ToolNames(), name, "channel %q", channel)
		}
		require.Contains(t, current.ToolNames(), "web_fetch")
	}
	for _, name := range []string{"web_search", "read", "write"} {
		require.NotContains(t, s.app.ToolNames(), name)
	}
	fresh := config.Default()
	fresh.LoadRuntimeOverrides(s.app.Config().DataDir)
	require.False(t, fresh.EnableWebSearch)
	require.Contains(t, fresh.EffectiveBlockedTools(), "read")
	w := request("GET", "", "settings-test")
	var response struct {
		Tools         []config.BuiltinToolSetting `json:"tools"`
		EffectiveFrom string                      `json:"effective_from"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, "new_session", response.EffectiveFrom)
	require.Len(t, response.Tools, 5)
	for _, id := range []string{"web_search", "file_read", "file_write", "bash"} {
		w := request("POST", `{"tool":"`+id+`","enabled":true}`, "settings-test")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	current, err := s.app.NewSession(ctx)
	require.NoError(t, err)
	for _, name := range []string{"web_search", "read", "write", "bash"} {
		require.Contains(t, current.ToolNames(), name)
	}
	require.NotContains(t, previous.ToolNames(), "bash", "existing sessions do not gain new tools")
	require.Equal(t, http.StatusOK, request("POST", `{"tool":"web_search","enabled":false}`, "settings-test").Code)
	require.NoError(t, os.WriteFile(filepath.Join(s.app.Config().DataDir, "settings.json"), []byte("corrupt"), 0o600))
	require.Equal(t, http.StatusInternalServerError, request("POST", `{"tool":"web_search","enabled":true}`, "settings-test").Code)
	require.False(t, s.app.Config().EnableWebSearch, "failed save must not publish a change")
}

func TestBuiltinToolStartupFlagCannotBeChangedThroughAPI(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir, cfg.Workspace = t.TempDir(), t.TempDir()
	cfg.Provider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "fixture", ""
	cfg.MarkBuiltinToolCommandLine("web_search")
	a, err := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })
	s := New(a, nil)
	r := localReq(http.MethodPost, "/settings/builtin-tools", strings.NewReader(`{"tool":"web_search","enabled":false}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	require.Equal(t, http.StatusConflict, w.Code)
	require.True(t, a.Config().EnableWebSearch)
}
