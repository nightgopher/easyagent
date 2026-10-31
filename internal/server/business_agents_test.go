package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/stretchr/testify/require"
)

func businessRequest(t *testing.T, s *Server, method, path string, body any, status int) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, localReq(method, path, bytes.NewReader(data)))
	require.Equal(t, status, w.Code, w.Body.String())
	return w
}

func TestBusinessAgentsCRUDAndSessionBinding(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	w := businessRequest(t, s, "POST", "/agents", map[string]any{"name": "销售", "instructions": "汇总客户反馈"}, 201)
	var a agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &a))
	businessRequest(t, s, "GET", "/agents/"+a.ID, nil, 200)
	businessRequest(t, s, "GET", "/agents", nil, 200)
	businessRequest(t, s, "GET", "/agents/missing", nil, 404)
	w = businessRequest(t, s, "POST", "/sessions", map[string]any{"agent_id": a.ID}, 200)
	var session SessionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &session))
	w = businessRequest(t, s, "GET", "/sessions/"+session.ID+"/info", nil, 200)
	require.Contains(t, w.Body.String(), a.ID)
	w = businessRequest(t, s, "GET", "/sessions", nil, 200)
	require.Contains(t, w.Body.String(), a.ID)
	w = businessRequest(t, s, "POST", "/sessions/"+session.ID+"/fork", map[string]any{}, 201)
	var fork forkResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &fork))
	id, err := s.app.SessionManager().AgentID(fork.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, id)
	a.Name = "售后"
	businessRequest(t, s, "PUT", "/agents/"+a.ID, a, 200)
	businessRequest(t, s, "PUT", "/agents/"+a.ID, a, 409)
	businessRequest(t, s, "POST", "/agents", map[string]any{"name": "Missing role"}, 400)
	businessRequest(t, s, "POST", "/agents", map[string]any{"name": "x", "instructions": "y", "unexpected": true}, 400)
	businessRequest(t, s, "POST", "/sessions", map[string]any{"agent_id": "missing"}, 404)
	businessRequest(t, s, "POST", "/sessions", map[string]any{"agent_id": a.ID, "cwd": t.TempDir()}, 400)
	s.SetAPIKey("owner-token")
	businessRequest(t, s, "GET", "/agents", nil, 401)
	businessRequest(t, s, "POST", "/agents", a, 401)
}
