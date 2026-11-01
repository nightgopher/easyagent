package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

func TestPermissionDefaultsPersistAndDoNotChangeExistingSessions(t *testing.T) {
	srv, server, _ := newRunTestServer(t)
	request := func(method, path, body string, status int) map[string]any {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, status, resp.StatusCode)
		var data map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&data))
		return data
	}
	settings := request("GET", "/settings/permissions", "", 200)
	require.Equal(t, "ask", settings["default_mode"])
	require.Equal(t, "full", settings["feishu_mode"])
	feishu := request("POST", "/sessions", `{"channel":"feishu"}`, 200)["id"].(string)
	web := request("POST", "/sessions", `{}`, 200)["id"].(string)
	require.Equal(t, "full", request("GET", "/sessions/"+feishu+"/info", "", 200)["access_mode"])
	require.Equal(t, "ask", request("GET", "/sessions/"+web+"/info", "", 200)["access_mode"])
	for _, bad := range []string{`{}`, `{"feishu_mode":"auto"}`, `{"default_mode":"full","feishu_mode":"bad"}`, `{"other":true}`, `{"default_mode":"full"} {}`} {
		request("POST", "/settings/permissions", bad, 400)
	}
	request("POST", "/settings/permissions", `{"default_mode":"full","feishu_mode":"ask"}`, 200)
	request("POST", "/sessions/"+feishu+"/permissions", `{"mode":"ask"}`, 200)
	require.NoError(t, srv.app.SessionStore().Delete(feishu))
	loaded, err := srv.app.LoadSession(context.Background(), feishu)
	require.NoError(t, err)
	require.Equal(t, "ask", loaded.AccessMode(), "explicit choice survives session reload")
	loaded, err = srv.app.LoadSession(context.Background(), web)
	require.NoError(t, err)
	require.Equal(t, "ask", loaded.AccessMode(), "saving defaults cannot promote an existing session")
	newID := request("POST", "/sessions", `{"channel":"feishu"}`, 200)["id"].(string)
	require.Equal(t, "ask", request("GET", "/sessions/"+newID+"/info", "", 200)["access_mode"])
	cfg := config.Default()
	cfg.LoadRuntimeOverrides(srv.app.Config().DataDir)
	require.Equal(t, "full", cfg.InitialAccessMode())
	require.Equal(t, "ask", cfg.ChannelAccessModes["feishu"])
}

func TestFeishuDefaultsAllowWritesAndAskBlocksUntilExplicitDecision(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		approve    bool
	}{{"full", "full", true}, {"ask deny", "ask", false}, {"ask allow", "ask", true}} {
		t.Run(test.name, func(t *testing.T) {
			srv, server, gateway := newFileRunTestServer(t, "approved.txt")
			general := "ask"
			require.NoError(t, config.SaveAccessModeDefaults(srv.app.Config().DataDir, &general, map[string]string{"feishu": test.mode}))
			srv.app.ReloadRuntimeOverrides()
			close(gateway.finish)
			run, _, err := srv.startChannelRun("", "write file", "feishu-write", "feishu")
			require.NoError(t, err)
			file := filepath.Join(srv.app.Config().Workspace, "approved.txt")
			if test.mode == "ask" {
				require.Eventually(t, func() bool { srv.runs.mu.Lock(); defer srv.runs.mu.Unlock(); return len(run.pending) == 1 }, 3*time.Second, time.Millisecond)
				_, err := os.Stat(file)
				require.True(t, os.IsNotExist(err))
				// Reconnecting SSE must deliver the still-pending request.
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/chat/stream", strings.NewReader(`{"prompt":"write file","request_id":"feishu-write","channel":"feishu"}`))
				resp, err := server.Client().Do(req)
				require.NoError(t, err)
				defer resp.Body.Close()
				snapshot, err := srv.runSnapshot(run.SessionID, 0, run.ID, nil)
				require.NoError(t, err)
				id := snapshot.PendingConfirmations[0].ID
				require.NoError(t, srv.confirmRun(run.SessionID, run.ID, id, test.approve, "explicit decision"))
				data, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Contains(t, string(data), "event: confirmation\n")
				require.Contains(t, string(data), "event: confirmed\n")
				require.Contains(t, string(data), fmt.Sprintf(`"approved":%t`, test.approve))
				require.Error(t, srv.confirmRun(run.SessionID, run.ID, id, true, "replay"))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err = srv.waitRun(ctx, run)
			require.NoError(t, err)
			_, err = os.Stat(file)
			if test.approve {
				require.NoError(t, err)
			} else {
				require.True(t, os.IsNotExist(err))
			}
		})
	}
}

func TestFeishuApprovalTimeoutDoesNotWrite(t *testing.T) {
	srv, _, gateway := newFileRunTestServer(t, "never.txt")
	mode := "ask"
	require.NoError(t, config.SaveAccessModeDefaults(srv.app.Config().DataDir, &mode, map[string]string{"feishu": "ask"}))
	srv.app.ReloadRuntimeOverrides()
	srv.runs.confirmationTimeout = 20 * time.Millisecond
	close(gateway.finish)
	run, _, err := srv.startChannelRun("", "write file", "timeout", "feishu")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = srv.waitRun(ctx, run)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(srv.app.Config().Workspace, "never.txt"))
	require.True(t, os.IsNotExist(err), "timed out approvals must never execute")
}
