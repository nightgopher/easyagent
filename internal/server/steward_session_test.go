package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/stretchr/testify/require"
)

type stewardSessionResponse struct {
	SessionID string `json:"session_id"`
	Created   bool   `json:"created"`
}

func TestStewardSessionResolveCreateReuseAndConsolidate(t *testing.T) {
	s, _, _ := newRunTestServer(t)

	// 首次调用：创建管家会话。
	w := businessRequest(t, s, "POST", "/agents/steward/session", nil, 200)
	var first stewardSessionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &first))
	require.True(t, first.Created)
	require.NotEmpty(t, first.SessionID)

	// 复用：再次调用返回同一会话，不新建。
	w = businessRequest(t, s, "POST", "/agents/steward/session", nil, 200)
	var again stewardSessionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &again))
	require.False(t, again.Created)
	require.Equal(t, first.SessionID, again.SessionID)

	// 历史遗留的第二个管家会话（白盒直建）被收敛归档，返回最新的那个。
	stray, err := s.createSessionRecord(context.Background(), "business", agentdata.StewardID, "", "")
	require.NoError(t, err)
	require.NotEqual(t, first.SessionID, stray)
	w = businessRequest(t, s, "POST", "/agents/steward/session", nil, 200)
	var third stewardSessionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &third))
	require.False(t, third.Created)
	require.Equal(t, stray, third.SessionID)

	sessions, err := s.app.SessionManager().List(context.Background())
	require.NoError(t, err)
	archived := map[string]bool{}
	for _, info := range sessions {
		if info.AgentID == agentdata.StewardID {
			archived[info.ID] = info.Archived
		}
	}
	require.Equal(t, map[string]bool{first.SessionID: true, stray: false}, archived)

	// 会话身份正确落在管家 agent 上。
	id, err := s.app.SessionManager().AgentID(third.SessionID)
	require.NoError(t, err)
	require.Equal(t, agentdata.StewardID, id)
}

func TestCreateSessionRejectsManagedStewardAgent(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	w := businessRequest(t, s, "POST", "/sessions", map[string]any{"agent_id": agentdata.StewardID}, 409)
	require.Contains(t, w.Body.String(), "/agents/steward/session")
	// 空会话没有因为守卫失败而落盘。
	sessions, err := s.app.SessionManager().List(context.Background())
	require.NoError(t, err)
	for _, info := range sessions {
		require.NotEqual(t, agentdata.StewardID, info.AgentID)
	}
}

func TestStewardSessionEndpointRejectsDisabledSteward(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	w := businessRequest(t, s, "POST", "/agents/steward", map[string]any{}, 200)
	var steward agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &steward))
	steward.Disabled = true
	businessRequest(t, s, "PUT", "/agents/"+steward.ID, steward, 200)
	w = businessRequest(t, s, "POST", "/agents/steward/session", nil, 409)
	require.Contains(t, w.Body.String(), "disabled")
}
