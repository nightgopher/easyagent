package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/internal/agents/coding"
	"github.com/nightgopher/easyagent/sdk/slashcmd"
	"github.com/stretchr/testify/require"
)

func TestWorkflowStillRunsInCodingSession(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	s.slashCmds = slashcmd.NewRegistry()
	coding.RegisterCommands(s.slashCmds)
	id, err := s.createSessionRecord(context.Background(), "coding", "", "", "")
	require.NoError(t, err)
	w := businessRequest(t, s, "POST", "/sessions/"+id+"/command", map[string]string{"command": "/workflow 测试"}, 200)
	var response CommandResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.True(t, response.ShouldQuery)
	require.Equal(t, "/workflow 测试", response.QueryPrompt)
	w = businessRequest(t, s, "POST", "/sessions/"+id+"/command", map[string]string{"command": "/workflow"}, 200)
	require.Contains(t, w.Body.String(), "用法")
}

func TestStewardWorkflowDispatchesCodingSession(t *testing.T) {
	s, _, gateway := newRunTestServer(t)
	s.slashCmds = slashcmd.NewRegistry()
	coding.RegisterCommands(s.slashCmds)
	w := businessRequest(t, s, "POST", "/agents/steward/session", nil, 200)
	var steward stewardSessionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &steward))
	w = businessRequest(t, s, "POST", "/sessions/"+steward.SessionID+"/command", map[string]string{"command": "/workflow 比较两个方案"}, 200)
	var response CommandResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.NotEmpty(t, response.SessionID)
	require.NotEqual(t, steward.SessionID, response.SessionID)
	require.False(t, response.ShouldQuery)
	require.Contains(t, response.Output, "已创建编程会话")
	select {
	case request := <-gateway.requests:
		data, err := json.Marshal(request)
		require.NoError(t, err)
		require.Contains(t, string(data), "/workflow 比较两个方案")
		require.Contains(t, string(data), "create_workflow")
	case <-time.After(10 * time.Second):
		t.Fatal("workflow task was not delivered to coding session")
	}
}

func TestWorkflowRejectsOtherBusinessAgents(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	s.slashCmds = slashcmd.NewRegistry()
	coding.RegisterCommands(s.slashCmds)
	w := businessRequest(t, s, "POST", "/agents", map[string]any{"name": "助手", "instructions": "帮助用户"}, 201)
	var definition struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &definition))
	w = businessRequest(t, s, "POST", "/sessions", map[string]any{"agent_id": definition.ID}, 200)
	var session SessionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &session))
	w = businessRequest(t, s, "POST", "/sessions/"+session.ID+"/command", map[string]string{"command": "/workflow 测试"}, 400)
	require.Contains(t, w.Body.String(), "编程会话或管家")
}
