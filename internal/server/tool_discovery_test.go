package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/internal/app"
	"github.com/stretchr/testify/require"
)

func TestToolDiscoverySettingsAuthPersistenceIsolationAndFailure(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	s.SetAPIKey("settings-test")
	request := func(method, body, token string) *httptest.ResponseRecorder {
		r := localReq(method, "/settings/tool-discovery", strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{"GET", "POST"} {
		require.Equal(t, http.StatusUnauthorized, request(method, `{"mode":"local"}`, "").Code)
	}
	w := request("GET", "", "settings-test")
	require.Equal(t, 200, w.Code)
	var initial app.ToolDiscoverySettings
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &initial))
	require.Equal(t, "off", initial.Mode)
	for _, body := range []string{`{}`, `null`, `{"mode":"invalid"}`, `{"mode":"local","token":"not-allowed"}`, `{"mode":"local"} {}`} {
		require.Equal(t, 400, request("POST", body, "settings-test").Code)
	}
	w = request("POST", `{"mode":"local"}`, "settings-test")
	require.Equal(t, 200, w.Code)
	var saved app.ToolDiscoverySettings
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &saved))
	require.Equal(t, "local", saved.Mode)
	require.Greater(t, saved.Revision, initial.Revision)
	require.Equal(t, "next_run", saved.EffectiveFrom)
	cfg := s.app.Config()
	cfg.ToolSearch = "off"
	cfg.LoadRuntimeOverrides(cfg.DataDir)
	require.Equal(t, "local", cfg.ToolSearch)
	other, _, _ := newRunTestServer(t)
	require.Equal(t, "off", other.app.ToolDiscoverySettings().Mode)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.DataDir, "settings.json"), []byte("corrupt settings"), 0600))
	require.Equal(t, 500, request("POST", `{"mode":"off"}`, "settings-test").Code)
	require.Equal(t, saved, s.app.ToolDiscoverySettings(), "failed persistence cannot publish a new policy")
}
