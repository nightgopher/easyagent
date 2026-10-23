package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionListReportsLiveRunState(t *testing.T) {
	application := newTestApp(t)
	busy, err := application.LoadOrCreateSession(context.Background(), "")
	require.NoError(t, err)
	idle, err := application.LoadOrCreateSession(context.Background(), "")
	require.NoError(t, err)
	srv := New(application, nil)
	srv.runs.mu.Lock()
	state := srv.runs.sessionLocked(busy.SessionID())
	state.run = &sessionRun{ID: "r1", SessionID: busy.SessionID(), State: "waiting_confirmation", pending: map[string]*pendingConfirmation{"c1": {ID: "c1"}}}
	srv.runs.mu.Unlock()

	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, localReq(http.MethodGet, "/sessions", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	byID := map[string]map[string]any{}
	for _, s := range list {
		byID[s["id"].(string)] = s
	}
	require.Equal(t, "waiting_confirmation", byID[busy.SessionID()]["run_state"])
	require.Equal(t, float64(1), byID[busy.SessionID()]["pending_confirmations"])
	require.NotContains(t, byID[idle.SessionID()], "run_state")
	require.Contains(t, byID[idle.SessionID()], "last_active", "existing fields stay at the top level")
}
