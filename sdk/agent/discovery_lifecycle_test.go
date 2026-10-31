package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/compaction"
	"github.com/nightgopher/easyagent/sdk/session"
	"github.com/stretchr/testify/require"
)

type approvedDiscoveryTool struct {
	discoveryTestTool
	executions int
}

func (t *approvedDiscoveryTool) RequiresConfirmation(json.RawMessage) (string, bool) {
	return "Approve business action", true
}
func (t *approvedDiscoveryTool) RequiresConfirmationAvailable() bool { return true }
func (t *approvedDiscoveryTool) Execute(context.Context, json.RawMessage, func(PartialResult)) (ToolResult, error) {
	t.executions++
	return ToolResult{Content: "executed"}, nil
}

func TestDiscoveryUsesOriginalApprovalAndProtectsFactsFromHooks(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(fmt.Sprint(approve), func(t *testing.T) {
			tool := &approvedDiscoveryTool{discoveryTestTool: discoveryTestTool{name: "business", info: ToolDiscoveryInfo{Deferred: true, SourceID: "mcp:business"}}}
			p := &recordingProvider{inner: &mockTestProvider{responses: []mockTestResponse{
				{toolCalls: []ai.ToolCall{{ID: "s1", Name: SearchToolsName, Args: `{"query":"business"}`}}, stop: ai.StopReasonToolUse},
				{toolCalls: []ai.ToolCall{{ID: "b1", Name: "business", Args: `{}`}}, stop: ai.StopReasonToolUse},
				{text: "done", stop: ai.StopReasonStop},
			}}}
			registry := providers.NewRegistry()
			registry.Register(p)
			confirmations := 0
			a := New(Options{Registry: registry, Model: ai.Model{ID: "test", Provider: p.Name(), ContextWindow: 128000}, Tools: []Tool{tool}, ToolSearchMode: ToolSearchLocal,
				ConfirmFunc: func(context.Context, ConfirmationRequest) ConfirmDecision {
					confirmations++
					return ConfirmDecision{Approved: approve}
				},
				LifecycleHooks: LifecycleHooks{After: []AfterToolCallHook{func(_ context.Context, call ToolCallContext, result ToolResult) (ToolResult, error) {
					if call.ToolName == SearchToolsName {
						result.Details = map[string]any{"kind": "tool_discovery", "version": 1, "selected": []string{"other"}}
					}
					return result, nil
				}}},
			})
			_, err := a.Prompt(context.Background(), ai.NewTextUserMessage("run business"))
			require.NoError(t, err)
			require.Len(t, p.requests, 3)
			require.NotContains(t, definitionNames(p.requests[0]), "business")
			require.Contains(t, definitionNames(p.requests[1]), "business")
			require.Equal(t, 1, confirmations)
			if approve {
				require.Equal(t, 1, tool.executions)
			} else {
				require.Zero(t, tool.executions)
			}
		})
	}
}

type failingDiscoveryStorage struct{ session.SessionStorage }

func (s *failingDiscoveryStorage) Append(ctx context.Context, entry session.Entry) error {
	if entry.Tool != nil {
		return errors.New("discovery disk failure")
	}
	return s.SessionStorage.Append(ctx, entry)
}

type partialDiscoveryStorage struct {
	session.SessionStorage
	results int
	cancel  context.CancelFunc
}

func (s *partialDiscoveryStorage) Append(ctx context.Context, entry session.Entry) error {
	if entry.Tool != nil {
		s.results++
		if s.results == 2 {
			return errors.New("second discovery write failed")
		}
	}
	err := s.SessionStorage.Append(ctx, entry)
	if err == nil && entry.Tool != nil && s.cancel != nil {
		s.cancel()
	}
	return err
}

func TestDiscoveryRetainsOnlyCommittedFactsOnCancelOrPartialWrite(t *testing.T) {
	for _, cancelAfterCommit := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelAfterCommit), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			storage := session.NewJSONLStorage(filepath.Join(t.TempDir(), "history.jsonl"))
			require.NoError(t, storage.Init())
			defer func() { require.NoError(t, storage.Close()) }()
			backend := &partialDiscoveryStorage{SessionStorage: storage}
			calls := []ai.ToolCall{{ID: "s1", Name: SearchToolsName, Args: `{"query":"first"}`}}
			if cancelAfterCommit {
				backend.cancel = cancel
			} else {
				calls = append(calls, ai.ToolCall{ID: "s2", Name: SearchToolsName, Args: `{"query":"second"}`})
			}
			p := &recordingProvider{inner: &mockTestProvider{responses: []mockTestResponse{{toolCalls: calls, stop: ai.StopReasonToolUse}}}}
			registry := providers.NewRegistry()
			registry.Register(p)
			tools := []Tool{
				&discoveryTestTool{name: "first", info: ToolDiscoveryInfo{Deferred: true}},
				&discoveryTestTool{name: "second", info: ToolDiscoveryInfo{Deferred: true}},
			}
			a := New(Options{Tools: tools, ToolSearchMode: ToolSearchLocal, Registry: registry, Model: ai.Model{ID: "test", Provider: p.Name(), ContextWindow: 128000}, Session: session.New(backend)})
			confirmed := 0
			a.Subscribe(func(_ context.Context, event AgentEvent) {
				if e, ok := event.(EventToolExecutionEnd); ok && e.ToolName == SearchToolsName && !e.IsError {
					confirmed++
				}
			})
			_, err := a.Prompt(ctx, ai.NewTextUserMessage("find tools"))
			if cancelAfterCommit {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorContains(t, err, "second discovery write failed")
			}
			require.Len(t, p.requests, 1)
			require.Equal(t, 1, confirmed)
			history, err := a.session.BuildContext(context.Background())
			require.NoError(t, err)
			require.Equal(t, []string{"first"}, a.discovery.loadedNames(history))
		})
	}
}

func TestDiscoveryFailureDoesNotPublishLoadedOrMakeNextRequest(t *testing.T) {
	for _, failure := range []string{"storage", "hook"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			storage := session.NewJSONLStorage(filepath.Join(t.TempDir(), "session.jsonl"))
			require.NoError(t, storage.Init())
			t.Cleanup(func() { require.NoError(t, storage.Close()) })
			var backend session.SessionStorage = storage
			if failure == "storage" {
				backend = &failingDiscoveryStorage{storage}
			}
			p := &recordingProvider{inner: &mockTestProvider{responses: []mockTestResponse{
				{toolCalls: []ai.ToolCall{{ID: "s1", Name: SearchToolsName, Args: `{"query":"请假"}`}}, stop: ai.StopReasonToolUse},
				{text: "done", stop: ai.StopReasonStop},
			}}}
			registry := providers.NewRegistry()
			registry.Register(p)
			a := New(Options{Registry: registry, Model: ai.Model{ID: "test", Provider: p.Name(), ContextWindow: 128000}, Tools: discoveryTools(), ToolSearchMode: ToolSearchLocal, Session: session.New(backend)})
			if failure == "hook" {
				a.lifecycleHooks.After = []AfterToolCallHook{func(context.Context, ToolCallContext, ToolResult) (ToolResult, error) {
					return ToolResult{}, errors.New("hook failure")
				}}
			}
			successEnds := 0
			a.Subscribe(func(_ context.Context, event AgentEvent) {
				if e, ok := event.(EventToolExecutionEnd); ok && e.ToolName == SearchToolsName && !e.IsError {
					successEnds++
				}
			})
			_, err := a.Prompt(ctx, ai.NewTextUserMessage("approval tools"))
			if failure == "storage" {
				require.ErrorContains(t, err, "discovery disk failure")
				require.Len(t, p.requests, 1)
			} else {
				require.NoError(t, err)
				require.Len(t, p.requests, 2)
				require.NotContains(t, definitionNames(p.requests[1]), "mcp__oa__pending")
			}
			require.Zero(t, successEnds)
			history, err := a.session.BuildContext(ctx)
			require.NoError(t, err)
			require.Empty(t, a.discovery.loadedNames(history))
		})
	}
}

func TestDiscoveryHistorySurvivesMicroCompactionAndBranchRestore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.jsonl")
	storage := session.NewJSONLStorage(path)
	require.NoError(t, storage.Init())
	sess := session.New(storage)
	require.NoError(t, sess.AppendMessage(ctx, ai.NewTextUserMessage("begin")))
	before, err := storage.GetLeaf(ctx)
	require.NoError(t, err)
	a := New(Options{Tools: discoveryTools(), ToolSearchMode: ToolSearchLocal, Session: sess})
	h := discoveryHistory(t, a, `{"query":"请假"}`)
	tr := h[1].(ai.ToolResultMessage)
	tr.Content = strings.Repeat("large result ", 4000)
	h[1] = tr
	for _, m := range h {
		require.NoError(t, sess.AppendMessage(ctx, m))
	}
	for i := 0; i < 5; i++ {
		require.NoError(t, sess.AppendMessage(ctx, ai.NewTextUserMessage("next")))
		id := fmt.Sprintf("read-%d", i)
		require.NoError(t, sess.AppendMessage(ctx, ai.AssistantMessage{ToolCalls: []ai.ToolCall{{ID: id, Name: "read", Args: `{}`}}}))
		require.NoError(t, sess.AppendMessage(ctx, ai.ToolResultMessage{ToolCallID: id, Content: "done"}))
	}
	history, err := sess.BuildContext(ctx)
	require.NoError(t, err)
	compressed, cleared := compaction.MicroCompact(history, 0)
	require.Positive(t, cleared)
	require.NoError(t, sess.AppendMicroCompaction(ctx, compressed))
	branch, err := storage.GetLeaf(ctx)
	require.NoError(t, err)
	require.NoError(t, storage.Close())
	storage = session.NewJSONLStorage(path)
	require.NoError(t, storage.Init())
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	restored := session.New(storage)
	require.NoError(t, restored.InitFromStorage(ctx))
	a = New(Options{Tools: discoveryTools(), ToolSearchMode: ToolSearchLocal, Session: restored})
	snapshot, err := a.ContextSnapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Tools, 3)
	require.Equal(t, 1, snapshot.Usage.ToolDiscovery.Loaded)
	require.NoError(t, restored.MoveTo(ctx, before, ""))
	snapshot, err = a.ContextSnapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Tools, 2)
	require.NoError(t, restored.MoveTo(ctx, branch, ""))
	require.NoError(t, restored.AppendCompactionKeeping(ctx, "old discovery summarized", []ai.Message{ai.NewTextUserMessage("continue")}, nil))
	snapshot, err = a.ContextSnapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Tools, 2)
}

func TestDiscoveryBudgetSerialAdmissionAndOversizedSchema(t *testing.T) {
	tools := discoveryTools()
	for _, tool := range tools[1:] {
		tool.(*discoveryTestTool).schema = map[string]any{"type": "object", "description": strings.Repeat("documentation ", 1500)}
	}
	a := New(Options{Tools: tools, ToolSearchMode: ToolSearchLocal, Model: ai.Model{ID: "budget", ContextWindow: 100000, MaxTokens: 1000}})
	one := discoveryHistory(t, a, `{"query":"mcp__oa__pending"}`)
	two := discoveryHistory(t, a, `{"query":"mcp__mail__send"}`)
	msg := two[0].(ai.AssistantMessage)
	msg.ToolCalls[0].ID = "search-2"
	two[0] = msg
	tr := two[1].(ai.ToolResultMessage)
	tr.ToolCallID = "search-2"
	two[1] = tr
	oneTokens := estimateContext(a.llmRequest(one), ai.Usage{}).Tools
	a.model.ContextWindow = oneTokens + 2500
	h := []ai.Message{ai.AssistantMessage{ToolCalls: append(one[0].(ai.AssistantMessage).ToolCalls, msg.ToolCalls...)}}
	first := a.admitDiscovery(h, one[1].(ai.ToolResultMessage))
	require.False(t, first.IsError)
	h = append(h, first)
	second := a.admitDiscovery(h, tr)
	require.True(t, second.IsError)
	require.Nil(t, second.Details)
	h = append(h, second)
	require.Len(t, a.discovery.loadedNames(h), 1)
	a.model.ContextWindow = 1000
	require.True(t, a.admitDiscovery(one[:1], one[1].(ai.ToolResultMessage)).IsError)
	require.Error(t, a.validateRequestBudget(a.llmRequest(h)))
}

func TestDiscoveryRejectsForgedUnpairedAndDeclinedResults(t *testing.T) {
	a := New(Options{Tools: discoveryTools(), ToolSearchMode: ToolSearchLocal})
	h := discoveryHistory(t, a, `{"query":"请假"}`)
	require.Empty(t, a.discovery.loadedNames(h[1:]))
	h[0] = ai.AssistantMessage{ToolCalls: []ai.ToolCall{{ID: "search-1", Name: "read", Args: `{}`}}}
	require.Empty(t, a.discovery.loadedNames(h))
	h[0] = ai.AssistantMessage{ToolCalls: []ai.ToolCall{{ID: "search-1", Name: "mcp__oa__pending", Args: `{}`}}}
	h[1] = ai.ToolResultMessage{ToolCallID: "search-1", Details: map[string]any{"approval": "declined"}}
	require.Empty(t, a.discovery.loadedNames(h))
	h[1] = ai.ToolResultMessage{ToolCallID: "search-1", Content: "done"}
	require.Equal(t, []string{"mcp__oa__pending"}, a.discovery.loadedNames(h))
}

func TestDiscoveryBrowsePaginationAndFixedCatalogSavings(t *testing.T) {
	var tools []Tool
	for i := 0; i < 500; i++ {
		tools = append(tools, &discoveryTestTool{name: fmt.Sprintf("mcp__service__tool_%03d", i), description: strings.Repeat("Business operation parameter documentation. ", 20), info: ToolDiscoveryInfo{Deferred: true, SourceID: fmt.Sprintf("mcp:service-%02d", i/20)}})
	}
	a := New(Options{Tools: tools, ToolSearchMode: ToolSearchLocal})
	s := a.tools[SearchToolsName].(*searchTools)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"query":""}`), nil)
	require.NoError(t, err)
	parts := strings.Split(result.Content, "Next cursor: ")
	require.Len(t, parts, 2)
	args, _ := json.Marshal(map[string]any{"query": "", "cursor": parts[1]})
	next, err := s.Execute(context.Background(), args, nil)
	require.NoError(t, err)
	require.Contains(t, next.Content, "service-24")
	require.NotContains(t, next.Content, "service-00")
	args, _ = json.Marshal(map[string]any{"query": "", "cursor": parts[1], "source": "mcp:service-00"})
	_, err = s.Execute(context.Background(), args, nil)
	require.Error(t, err)
	_, err = s.Execute(context.Background(), json.RawMessage(`{"query":"","cursor":"bad"}`), nil)
	require.Error(t, err)
	_, summary, err := DiscoveryPromptTools(tools, ToolSearchLocal)
	require.NoError(t, err)
	a.system = summary
	off := New(Options{Tools: tools})
	full, local := estimateContext(off.llmRequest(nil), ai.Usage{}), estimateContext(a.llmRequest(nil), ai.Usage{})
	require.Less(t, local.EstimatedTokens, full.EstimatedTokens/5)
	t.Logf("500-tool fixture: off=%d, local=%d estimated tool/manifest tokens (%.1f%% reduction)", full.EstimatedTokens, local.EstimatedTokens, 100*(1-float64(local.EstimatedTokens)/float64(full.EstimatedTokens)))
}
