package business

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/stretchr/testify/require"
)

func TestMemoryToolsBindIdentityAndWorkspace(t *testing.T) {
	ctx := context.Background()
	store := agentdata.New(t.TempDir())
	a, err := store.SaveAgent(ctx, agentdata.Agent{Name: "Support", Instructions: "Role", MemoryEnabled: true})
	require.NoError(t, err)
	b, err := store.SaveAgent(ctx, agentdata.Agent{Name: "Other", Instructions: "Role", MemoryEnabled: true})
	require.NoError(t, err)
	workspace := t.TempDir()
	tool := memoryTool{store: store, agentID: a.ID, workspace: workspace, sessionID: "source-session", action: "save"}
	result, err := tool.Execute(ctx, json.RawMessage(`{"key":"language","content":"Use English","agent_id":"forged","source_kind":"user"}`), nil)
	require.NoError(t, err)
	var m agentdata.Memory
	require.NoError(t, json.Unmarshal([]byte(result.Content), &m))
	require.Equal(t, a.ID, m.AgentID)
	require.Equal(t, "agent", m.SourceKind)
	require.Equal(t, "source-session", m.SourceSession)
	tool.action = "search"
	result, err = tool.Execute(ctx, json.RawMessage(`{"query":"English"}`), nil)
	require.NoError(t, err)
	require.Contains(t, result.Content, "Use English")
	tool.agentID = b.ID
	result, err = tool.Execute(ctx, json.RawMessage(`{"query":"English"}`), nil)
	require.NoError(t, err)
	require.Equal(t, "[]", result.Content)
	tool.action = "forget"
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	_, err = tool.Execute(ctx, raw, nil)
	require.Error(t, err)
	tool.agentID = a.ID
	_, err = tool.Execute(ctx, raw, nil)
	require.NoError(t, err)
	application := Application{Definition: a, Store: store}
	tools := application.BuildTools(runtime.ToolBuildOptions{Workspace: workspace, BlockedTools: []string{"memory_save"}})
	names := []string{}
	for _, t := range tools {
		names = append(names, t.Name())
	}
	require.Contains(t, names, "memory_search")
	require.NotContains(t, names, "memory_save")
}
