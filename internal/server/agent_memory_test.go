package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/stretchr/testify/require"
)

func TestAgentMemoryAPIIsScopedAndVersioned(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	ctx := context.Background()
	a, err := s.app.Agents().SaveAgent(ctx, agentdata.Agent{Name: "Support", Instructions: "Role", MemoryEnabled: true})
	require.NoError(t, err)
	b, err := s.app.Agents().SaveAgent(ctx, agentdata.Agent{Name: "Sales", Instructions: "Role", MemoryEnabled: true})
	require.NoError(t, err)
	base := "/agents/" + a.ID + "/memories"
	w := businessRequest(t, s, "POST", base, map[string]any{"key": "联系偏好", "content": "使用邮件", "agent_id": b.ID, "source_kind": "agent", "source_session": "forged"}, 200)
	var memory agentdata.Memory
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &memory))
	require.Equal(t, a.ID, memory.AgentID)
	require.Equal(t, "user", memory.SourceKind)
	require.Empty(t, memory.SourceSession)
	businessRequest(t, s, "GET", base, nil, 200)
	businessRequest(t, s, "GET", base+"/"+memory.ID, nil, 200)
	businessRequest(t, s, "GET", "/agents/"+b.ID+"/memories/"+memory.ID, nil, 404)
	before := memory.Version
	memory.Content = "改用电话"
	w = businessRequest(t, s, "PUT", base+"/"+memory.ID, memory, 200)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &memory))
	businessRequest(t, s, "DELETE", base+"/"+memory.ID, map[string]any{"version": before}, 409)
	businessRequest(t, s, "DELETE", base+"/"+memory.ID, map[string]any{"version": memory.Version}, 200)
	businessRequest(t, s, "GET", base+"/"+memory.ID, nil, 404)
	businessRequest(t, s, "POST", base, map[string]any{"key": "bad", "content": "fact", "scope": "everyone"}, 400)
	businessRequest(t, s, "GET", "/agents/missing/memories", nil, 404)
	s.SetAPIKey("owner")
	businessRequest(t, s, "GET", base, nil, 401)
}
