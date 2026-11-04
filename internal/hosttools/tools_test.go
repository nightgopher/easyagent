package hosttools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
	"github.com/stretchr/testify/require"
)

func TestHostReadToolsSearchArchivesAndHonorRevocation(t *testing.T) {
	ctx := context.Background()
	store, sessions := agentdata.New(t.TempDir()), sessionmgr.NewManager(t.TempDir())
	steward, err := store.EnsureSteward(ctx)
	require.NoError(t, err)
	specialist, err := store.SaveAgent(ctx, agentdata.Agent{Name: "客户助手", Instructions: "private-role-secret", MemoryEnabled: true})
	require.NoError(t, err)
	_, err = store.SaveMemory(ctx, specialist.ID, "", "user", "", agentdata.Memory{Key: "private", Content: "private-memory-secret"})
	require.NoError(t, err)
	id, _, err := sessions.Create(ctx)
	require.NoError(t, err)
	require.NoError(t, sessions.SaveAgentMeta(id, t.TempDir(), specialist.ID))
	archived := true
	title := "客户资料"
	require.NoError(t, sessions.UpdatePreferences(id, sessionmgr.PreferencePatch{Archived: &archived, Title: &title}))
	s, _, err := sessions.Open(ctx, id)
	require.NoError(t, err)
	defer s.Storage().Close()
	for range 23 {
		require.NoError(t, s.AppendMessage(ctx, ai.NewTextUserMessage("待交付报告")))
	}
	require.NoError(t, s.AppendMessage(ctx, ai.ToolResultMessage{Content: "private-tool-secret"}))
	tool := Tool{Store: store, Sessions: sessions, AgentID: steward.ID, Kind: "host_session_search"}
	result, err := tool.Execute(ctx, json.RawMessage(`{"query":"报告"}`), nil)
	require.NoError(t, err)
	var found struct {
		Hits  []map[string]any `json:"hits"`
		Total int              `json:"total"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content), &found))
	require.Len(t, found.Hits, 20)
	require.Equal(t, 23, found.Total)
	require.Equal(t, true, found.Hits[0]["archived"])
	require.Contains(t, found.Hits[0]["session_link"], SessionLink(id)+"?")
	for _, term := range []string{"private-memory-secret", "private-tool-secret", "private-role-secret"} {
		input, _ := json.Marshal(map[string]any{"query": term})
		result, err = tool.Execute(ctx, input, nil)
		require.NoError(t, err)
		require.Contains(t, result.Content, `"total":0`)
	}
	tool.Kind = "host_agents_list"
	result, err = tool.Execute(ctx, json.RawMessage(`{}`), nil)
	require.NoError(t, err)
	require.Contains(t, result.Content, "客户助手")
	require.NotContains(t, result.Content, "private-")
	require.NotContains(t, result.Content, "instructions")
	steward.Tools = []string{"local_time"}
	steward, err = store.SaveAgent(ctx, steward)
	require.NoError(t, err)
	_, err = tool.Execute(ctx, json.RawMessage(`{}`), nil)
	require.ErrorContains(t, err, "permission is disabled")
	steward.Tools, steward.Disabled = []string{tool.Kind}, true
	_, err = store.SaveAgent(ctx, steward)
	require.NoError(t, err)
	_, err = tool.Execute(ctx, json.RawMessage(`{}`), nil)
	require.ErrorContains(t, err, "permission is disabled")
}
