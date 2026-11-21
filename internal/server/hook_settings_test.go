package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/hooks"
	"github.com/stretchr/testify/require"
)

func TestServerCommandHookHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "deny-server-hook" {
		return
	}
	fmt.Print(`{"decision":"deny","reason":"blocked by configured hook"}`)
	os.Exit(0)
}

func TestHookSettingsControlRunningSessionAndRejectStaleEdit(t *testing.T) {
	srv, _, gateway := newFileRunTestServer(t, "hooked.txt")
	close(gateway.finish)
	request := func(method, path, body string, want int) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, localReq(method, path, strings.NewReader(body)))
		require.Equal(t, want, rec.Code, rec.Body.String())
		var result map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		return result
	}
	initial := request(http.MethodGet, "/settings/hooks", "", http.StatusOK)
	command, err := os.Executable()
	require.NoError(t, err)
	h := hooks.CommandHook{Event: "before_tool", Tool: "write", Command: command, Args: []string{"-test.run=^TestServerCommandHookHelper$", "deny-server-hook"}, Enabled: true}
	body := func(revision string, enabled bool) string {
		h.Enabled = enabled
		b, e := json.Marshal(map[string]any{"revision": revision, "hooks": []hooks.CommandHook{h}})
		require.NoError(t, e)
		return string(b)
	}
	updated := request(http.MethodPut, "/settings/hooks", body(initial["revision"].(string), true), http.StatusOK)
	request(http.MethodPut, "/settings/hooks", body(initial["revision"].(string), false), http.StatusConflict)
	run, _, err := srv.startRun("", "write once", "hook-run-one")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = srv.waitRun(ctx, run)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(srv.app.Config().Workspace, "hooked.txt"))
	require.True(t, os.IsNotExist(err), "before hook must block the actual write")
	request(http.MethodPut, "/settings/hooks", body(updated["revision"].(string), false), http.StatusOK)
	run, _, err = srv.startRun(run.SessionID, "write again", "hook-run-two")
	require.NoError(t, err)
	_, err = srv.waitRun(ctx, run)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(srv.app.Config().Workspace, "hooked.txt"))
	require.NoError(t, err, "updated settings apply to an existing session")
}

func TestHookSettingsRejectsOpenRemoteAccessWithoutOwnerToken(t *testing.T) {
	srv, _, _ := newFileRunTestServer(t, "")
	srv.apiKey, srv.allowNoAuth = "", true
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		req := httptest.NewRequest(method, "/settings/hooks", strings.NewReader(`{"hooks":[],"revision":"x"}`))
		req.RemoteAddr = "192.0.2.1:1234"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusForbidden, rec.Code)
	}
}
