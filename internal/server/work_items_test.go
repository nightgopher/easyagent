package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/stretchr/testify/require"
)

func TestWorkAPITracksExplicitSessionsAndProjectsRuntime(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	sess, err := s.app.NewSession(context.Background())
	require.NoError(t, err)
	path := "/work"
	w := businessRequest(t, s, "POST", path, map[string]any{"session_id": sess.SessionID(), "title": "准备演示"}, 201)
	var item workView
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &item))
	require.Equal(t, "active", item.State)
	businessRequest(t, s, "POST", path, map[string]any{"session_id": "missing", "title": "x"}, 404)
	businessRequest(t, s, "POST", path, map[string]any{"session_id": sess.SessionID(), "title": "x", "agent_id": "forged"}, 400)
	update := item.WorkUpdate
	update.State = "waiting_review"
	w = businessRequest(t, s, "PUT", path+"/"+item.ID, update, 200)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &item))
	require.Equal(t, "waiting_review", item.Attention)
	businessRequest(t, s, "PUT", path+"/"+item.ID, update, 409)
	// A turn ending does not close the work; a failed/interrupted run is surfaced.
	for _, state := range []string{"completed", "failed", "interrupted", "waiting_confirmation", "running"} {
		s.runs.mu.Lock()
		s.runs.sessionLocked(sess.SessionID()).run = &sessionRun{ID: "run-test", SessionID: sess.SessionID(), State: state, StartedAt: time.Now()}
		s.runs.mu.Unlock()
		w = businessRequest(t, s, "GET", path+"/"+item.ID, nil, 200)
		var view workView
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
		require.Equal(t, "waiting_review", view.State)
		require.Equal(t, state, view.RunState)
		if state == "running" {
			require.Empty(t, view.Attention)
		} else {
			require.NotEmpty(t, view.Attention)
		}
	}
	// Remove the test-only running projection before server cleanup.
	s.runs.mu.Lock()
	delete(s.runs.sessions, sess.SessionID())
	s.runs.mu.Unlock()
	w = businessRequest(t, s, "GET", "/work?view=attention&limit=1", nil, 200)
	require.Contains(t, w.Body.String(), item.ID)
	businessRequest(t, s, "GET", "/work?limit=-1", nil, 400)
	businessRequest(t, s, "GET", "/work?view=unknown", nil, 400)
	_, err = s.app.Agents().UpdateWork(context.Background(), item.ID, "", agentdata.WorkUpdate{Title: item.Title, State: "completed", Version: item.Version})
	require.NoError(t, err)
	w = businessRequest(t, s, "GET", "/work?view=attention", nil, 200)
	require.NotContains(t, w.Body.String(), item.ID)
	s.SetAPIKey("owner-token")
	businessRequest(t, s, "GET", "/work", nil, 401)
	businessRequest(t, s, "PUT", "/work/"+item.ID, update, 401)
}
