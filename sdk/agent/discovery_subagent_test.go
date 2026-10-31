package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/stretchr/testify/require"
)

type readonlyDiscoveryTool struct{ discoveryTestTool }

func (*readonlyDiscoveryTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func TestDiscoveryChildSearchCannotSeeParentWriteTools(t *testing.T) {
	safe := &readonlyDiscoveryTool{discoveryTestTool{name: "inspect", description: "READ_ONLY_CAPABILITY", info: ToolDiscoveryInfo{Deferred: true, SourceID: "builtin:inspect"}}}
	write := &discoveryTestTool{name: "erase", description: "PARENT_WRITE_CAPABILITY", info: ToolDiscoveryInfo{Deferred: true, SourceID: "builtin:erase"}}
	parent := New(Options{Tools: []Tool{safe, write}, ToolSearchMode: ToolSearchLocal})
	search, _ := parent.GetTool(SearchToolsName)
	p := &recordingProvider{inner: &mockTestProvider{responses: []mockTestResponse{
		{toolCalls: []ai.ToolCall{{ID: "s1", Name: SearchToolsName, Args: `{"query":"erase"}`}}, stop: ai.StopReasonToolUse},
		{toolCalls: []ai.ToolCall{{ID: "s2", Name: SearchToolsName, Args: `{"query":"inspect"}`}}, stop: ai.StopReasonToolUse},
		{text: "done", stop: ai.StopReasonStop},
		{text: "fresh", stop: ai.StopReasonStop},
	}}}
	registry := providers.NewRegistry()
	registry.Register(p)
	task := NewTaskTool(TaskToolOptions{Registry: registry, Model: ai.Model{ID: "test", Provider: p.Name(), ContextWindow: 128000}, Tools: []Tool{safe, write, search}, ToolSearchMode: ToolSearchLocal,
		BuildPrompt: func(tools []Tool) (string, error) {
			visible, summary, err := DiscoveryPromptTools(tools, ToolSearchLocal)
			var b strings.Builder
			for _, t := range visible {
				b.WriteString(t.Description())
			}
			return b.String() + summary, err
		},
	})
	task.SetSystemPrompt("PARENT_WRITE_CAPABILITY should not leak")
	_, err := task.Execute(context.Background(), json.RawMessage(`{"description":"readonly","prompt":"inspect","readonly":true}`), nil)
	require.NoError(t, err)
	require.Len(t, p.requests, 3)
	for _, request := range p.requests {
		require.NotContains(t, request.System, "PARENT_WRITE_CAPABILITY")
		require.NotContains(t, definitionNames(request), "erase")
	}
	require.NotContains(t, definitionNames(p.requests[1]), "inspect")
	require.Contains(t, definitionNames(p.requests[2]), "inspect")
	_, err = task.Execute(context.Background(), json.RawMessage(`{"description":"second","prompt":"fresh","readonly":true}`), nil)
	require.NoError(t, err)
	require.NotContains(t, definitionNames(p.requests[3]), "inspect", "another child must search independently")
	require.Empty(t, parent.discovery.loadedNames(nil))
}
