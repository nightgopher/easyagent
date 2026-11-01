package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/stretchr/testify/require"
)

func TestStewardAPIAndWorkToolUseRuntimeProjection(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	w := businessRequest(t, s, "POST", "/agents/steward", map[string]any{}, 200)
	var definition agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &definition))
	require.Equal(t, agentdata.StewardID, definition.ID)
	businessRequest(t, s, "POST", "/agents/steward", map[string]any{"tools": []string{"bash"}}, 400)
	ctx := context.Background()
	sess, err := s.app.NewSession(ctx)
	require.NoError(t, err)
	_, err = s.app.Agents().TrackWork(ctx, agentdata.WorkItem{SessionID: sess.SessionID(), WorkUpdate: agentdata.WorkUpdate{Title: "交付演示"}})
	require.NoError(t, err)
	s.runs.mu.Lock()
	s.runs.sessionLocked(sess.SessionID()).run = &sessionRun{ID: "failed-run", SessionID: sess.SessionID(), State: "failed", Error: "fixture failure"}
	s.runs.mu.Unlock()
	_, deps, err := s.app.SessionSetup(ctx, "", definition.ID, s.app.Config())
	require.NoError(t, err)
	found := false
	for _, tool := range deps.Application.BuildTools(runtime.ToolBuildOptions{Workspace: t.TempDir()}) {
		if tool.Name() != "host_work_search" {
			continue
		}
		found = true
		result, err := tool.Execute(ctx, json.RawMessage(`{"view":"attention"}`), nil)
		require.NoError(t, err)
		require.Contains(t, result.Content, `"run_state":"failed"`)
		require.Contains(t, result.Content, `"attention":"failed"`)
		require.Contains(t, result.Content, `easyagent://session/`+sess.SessionID())
	}
	require.True(t, found)
	s.runs.mu.Lock()
	delete(s.runs.sessions, sess.SessionID())
	s.runs.mu.Unlock()
	definition.Disabled = true
	_, err = s.app.Agents().SaveAgent(ctx, definition)
	require.NoError(t, err)
	businessRequest(t, s, "POST", "/agents/steward", map[string]any{}, 409)
	s.SetAPIKey("owner-token")
	businessRequest(t, s, "POST", "/agents/steward", map[string]any{}, 401)
}
