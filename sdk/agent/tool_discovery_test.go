package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/stretchr/testify/require"
)

type discoveryTestTool struct {
	name, description string
	info              ToolDiscoveryInfo
	schema            map[string]any
}

func (t *discoveryTestTool) Name() string        { return t.name }
func (t *discoveryTestTool) Description() string { return t.description }
func (t *discoveryTestTool) Parameters() map[string]any {
	if t.schema != nil {
		return t.schema
	}
	return map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}}}
}
func (t *discoveryTestTool) DiscoveryInfo() ToolDiscoveryInfo                    { return t.info }
func (t *discoveryTestTool) Validate(p json.RawMessage) (json.RawMessage, error) { return p, nil }
func (t *discoveryTestTool) Execute(context.Context, json.RawMessage, func(PartialResult)) (ToolResult, error) {
	return ToolResult{Content: "done"}, nil
}

func discoveryTools() []Tool {
	return []Tool{
		&discoveryTestTool{name: "read", description: "Read a file"},
		&discoveryTestTool{name: "mcp__oa__pending", description: "查询待审批的请假申请", info: ToolDiscoveryInfo{Deferred: true, SourceID: "mcp:oa", SourceLabel: "OA", Aliases: []string{"listPendingLeave"}}},
		&discoveryTestTool{name: "mcp__mail__send", description: "Send an email", info: ToolDiscoveryInfo{Deferred: true, SourceID: "mcp:mail"}},
	}
}

func definitionNames(req ai.StreamRequest) []string {
	var names []string
	for _, d := range req.Tools {
		names = append(names, d.Name)
	}
	return names
}

func discoveryHistory(t *testing.T, a *Agent, args string) []ai.Message {
	t.Helper()
	call := ai.ToolCall{ID: "search-1", Name: SearchToolsName, Args: args}
	result := executeOneTool(context.Background(), a, call).(ai.ToolResultMessage)
	require.False(t, result.IsError, result.Content)
	return []ai.Message{ai.AssistantMessage{ToolCalls: []ai.ToolCall{call}}, result}
}

func TestToolDiscoveryProjectionAndRecovery(t *testing.T) {
	tools := discoveryTools()
	a := New(Options{Tools: tools, ToolSearchMode: ToolSearchLocal})
	require.Equal(t, []string{"read", SearchToolsName}, definitionNames(a.llmRequest(nil)))
	for _, query := range []string{"请假审批", "listPendingLeave", "mcp__oa__pending"} {
		history := discoveryHistory(t, a, `{"query":"`+query+`"}`)
		require.Equal(t, []string{"read", SearchToolsName, "mcp__oa__pending"}, definitionNames(a.llmRequest(history)))
		// Persisted JSON details decode as maps; a fresh Agent must recover them.
		tr := history[1].(ai.ToolResultMessage)
		data, err := json.Marshal(tr.Details)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &tr.Details))
		history[1] = tr
		restored := New(Options{Tools: tools, ToolSearchMode: ToolSearchLocal})
		require.Equal(t, definitionNames(a.llmRequest(history)), definitionNames(restored.llmRequest(history)))
		require.Equal(t, definitionNames(a.llmRequest(nil)), definitionNames(a.llmRequest(history[:1])))
		tr.IsError = true
		history[1] = tr
		require.Equal(t, definitionNames(a.llmRequest(nil)), definitionNames(a.llmRequest(history)))
	}
	// Off remains full; discovery does not alter execution objects.
	off := New(Options{Tools: tools})
	require.ElementsMatch(t, []string{"read", "mcp__oa__pending", "mcp__mail__send"}, definitionNames(off.llmRequest(nil)))
	got, ok := a.GetTool("mcp__oa__pending")
	require.True(t, ok)
	require.Same(t, tools[1], got)
}

func TestToolDiscoveryBrowseFilterAndValidation(t *testing.T) {
	a := New(Options{Tools: discoveryTools(), ToolSearchMode: ToolSearchLocal})
	for _, args := range []string{`{"query":""}`, `{"query":"","source":"mcp:oa"}`, `{"query":"nonexistent"}`, `{"query":"请假","source":"mcp:mail"}`, `{"query":"read"}`} {
		h := discoveryHistory(t, a, args)
		require.Equal(t, definitionNames(a.llmRequest(nil)), definitionNames(a.llmRequest(h)))
	}
	search, _ := a.GetTool(SearchToolsName)
	for _, args := range []string{`null`, `{}`, `{"query":3}`, `{"query":"x","limit":0}`, `{"query":"x","limit":9}`, `{"query":"x","cursor":"bad"}`} {
		_, err := search.Validate(json.RawMessage(args))
		require.Error(t, err, args)
	}
	_, err := search.Validate(json.RawMessage(`{"query":"` + strings.Repeat("中", 513) + `"}`))
	require.Error(t, err)
}

func TestToolDiscoveryPromptDoesNotExpandDeferredDefinitions(t *testing.T) {
	tools, summary, err := DiscoveryPromptTools(discoveryTools(), ToolSearchLocal)
	require.NoError(t, err)
	require.Len(t, tools, 2)
	require.Contains(t, summary, "mcp:oa")
	require.Contains(t, summary, SearchToolsName)
	require.NotContains(t, summary, "properties")
	require.LessOrEqual(t, len([]rune(summary)), 2000)
	_, _, err = DiscoveryPromptTools([]Tool{&discoveryTestTool{name: SearchToolsName}}, ToolSearchLocal)
	require.Error(t, err)
}

func TestToolDiscoverySnapshotAndSchemaRefresh(t *testing.T) {
	tools := discoveryTools()
	tool := tools[1].(*discoveryTestTool)
	tool.schema = map[string]any{"type": "object", "properties": map[string]any{"before": map[string]any{"type": "string"}}}
	a := New(Options{Tools: tools, ToolSearchMode: ToolSearchLocal})
	h := discoveryHistory(t, a, `{"query":"请假"}`)
	tool.schema["properties"] = map[string]any{"after": map[string]any{"type": "number"}}
	req := a.llmRequest(h)
	require.Contains(t, req.Tools[2].Parameters.(map[string]any)["properties"], "before")
	b := New(Options{Tools: tools, ToolSearchMode: ToolSearchLocal})
	require.Contains(t, b.llmRequest(h).Tools[2].Parameters.(map[string]any)["properties"], "after")
	require.NotEqual(t, a.discovery.revision, b.discovery.revision)
	removed := New(Options{Tools: []Tool{tools[0], tools[2]}, ToolSearchMode: ToolSearchLocal})
	require.NotContains(t, definitionNames(removed.llmRequest(h)), tool.Name())
}

func TestToolDiscoverySnapshotPreservesLargeIntegerConstraints(t *testing.T) {
	tool := &discoveryTestTool{name: "large_id", info: ToolDiscoveryInfo{Deferred: true}, schema: map[string]any{
		"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer", "maximum": int64(9007199254740993)}},
	}}
	a := New(Options{Tools: []Tool{tool}, ToolSearchMode: ToolSearchLocal})
	history := discoveryHistory(t, a, `{"query":"large_id"}`)
	actual, err := json.Marshal(a.llmRequest(history).Tools[1].Parameters)
	require.NoError(t, err)
	expected, err := json.Marshal(tool.Parameters())
	require.NoError(t, err)
	require.Equal(t, string(expected), string(actual))
}
