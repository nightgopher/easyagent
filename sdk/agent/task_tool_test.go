package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hwj123hwj/easyagent/sdk/ai"
	"github.com/hwj123hwj/easyagent/sdk/ai/providers"
	"github.com/hwj123hwj/easyagent/sdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingProvider 按顺序回放脚本响应，并记录每次请求，用来检查父子上下文是否隔离。
type recordingProvider struct {
	mu            sync.Mutex
	inner         *mockTestProvider
	requests      []ai.StreamRequest
	cancelOnBlock context.CancelFunc
	blockFrom     int // 第几次调用起阻塞到 ctx 取消（0 表示不阻塞）
}

func TestTaskModelTurnsAndUsage(t *testing.T) {
	cached := 7
	p := &recordingProvider{inner: &mockTestProvider{responses: []mockTestResponse{
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "safe", Args: `{}`}}, stop: ai.StopReasonToolUse, usage: ai.Usage{InputTokens: 11, OutputTokens: 3, CachedInputTokens: &cached}},
		{text: "done", stop: ai.StopReasonStop, usage: ai.Usage{InputTokens: 17, OutputTokens: 5, CachedInputTokens: &cached}},
	}}}
	_, task := newTaskTestAgent(p, nil, &concurrentSafeTool{})
	result, err := task.Execute(context.Background(), json.RawMessage(`{"prompt":"inspect","model":"child-model","max_turns":1}`), nil)
	require.NoError(t, err)
	require.Len(t, p.requests, 1, "explicit limit prevents a second model request")
	require.Equal(t, "child-model", p.requests[0].Model.ID)
	require.Equal(t, "mock_test", p.requests[0].Model.Provider)
	require.Equal(t, ai.Usage{InputTokens: 11, OutputTokens: 3, CachedInputTokens: &cached}, result.SubagentUsage.Usage)
	require.Equal(t, "child-model", result.SubagentUsage.Model)
	for _, raw := range []string{`{"prompt":"x","max_turns":0}`, `{"prompt":"x","max_turns":-1}`, `{"prompt":"x","provider":"unknown"}`} {
		_, err := task.Validate(json.RawMessage(raw))
		require.Error(t, err)
	}
	_, err = task.Execute(context.Background(), json.RawMessage(`{"prompt":"x","model":"x","provider":"unknown"}`), nil)
	require.ErrorContains(t, err, "not registered")
}

func TestTaskUsageSurvivesFailureAndHooks(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			p := &recordingProvider{inner: &mockTestProvider{responses: []mockTestResponse{
				{toolCalls: []ai.ToolCall{{ID: "c1", Name: "safe", Args: `{}`}}, stop: ai.StopReasonToolUse, usage: ai.Usage{InputTokens: 11, OutputTokens: 3}},
				{text: "done", stop: ai.StopReasonStop, usage: ai.Usage{InputTokens: 17, OutputTokens: 5}},
			}}}
			if failure {
				p.blockFrom = 2
			}
			parent, task := newTaskTestAgent(p, nil, &concurrentSafeTool{})
			ctx, cancel := context.WithCancel(context.Background())
			p.cancelOnBlock = cancel
			defer cancel()
			if !failure {
				parent.lifecycleHooks.After = []AfterToolCallHook{func(_ context.Context, _ ToolCallContext, _ ToolResult) (ToolResult, error) {
					return ToolResult{}, errors.New("hook failed")
				}}
			}
			msg := executeOneTool(ctx, parent, ai.ToolCall{ID: "task-1", Name: task.Name(), Args: `{"prompt":"inspect"}`}).(ai.ToolResultMessage)
			require.True(t, msg.IsError)
			require.NotNil(t, msg.SubagentUsage)
			want := ai.Usage{InputTokens: 28, OutputTokens: 8}
			if failure {
				want = ai.Usage{InputTokens: 11, OutputTokens: 3}
			}
			require.Equal(t, want, msg.SubagentUsage.Usage)
		})
	}
}

func TestTaskUsagePersistsAcrossCompactionAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	storage := session.NewJSONLStorage(path)
	require.NoError(t, storage.Init())
	sess := session.New(storage)
	usage := ai.Usage{InputTokens: 110, OutputTokens: 20}
	msg := ai.ToolResultMessage{ToolCallID: "task-1", Content: "child answer", SubagentUsage: &ai.SubagentUsage{Model: "child", Provider: "mock_test", Usage: usage}}
	require.NoError(t, sess.AppendMessage(ctx, msg))
	boundary, err := storage.GetLeaf(ctx)
	require.NoError(t, err)
	require.NoError(t, sess.AppendCompactionKeeping(ctx, "summary", []ai.Message{msg}, nil))
	require.NoError(t, storage.Close())
	storage = session.NewJSONLStorage(path)
	require.NoError(t, storage.Init())
	t.Cleanup(func() { _ = storage.Close() })
	restored := session.New(storage)
	require.NoError(t, restored.InitFromStorage(ctx))
	total, err := restored.SubagentUsage(ctx, "")
	require.NoError(t, err)
	require.Equal(t, usage, total, "retained compaction copies must not count twice")
	later, err := restored.SubagentUsage(ctx, boundary)
	require.NoError(t, err)
	require.Equal(t, ai.Usage{}, later, "later runs must not recount earlier tasks")
	a := New(Options{Model: ai.Model{ID: "t"}, Session: restored})
	snapshot, err := a.ContextSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, usage, snapshot.Usage.SubagentUsage)
	require.Zero(t, snapshot.Usage.LastRequest.InputTokens)
	require.Less(t, snapshot.Usage.EstimatedTokens, usage.InputTokens)
}

func (p *recordingProvider) Name() string { return "mock_test" }
func (p *recordingProvider) StreamSimple(ctx context.Context, req ai.SimpleStreamRequest) (*ai.EventStream, error) {
	return p.Stream(ctx, ai.StreamRequest{Model: req.Model, Messages: req.Messages, System: req.System, Tools: req.Tools})
}
func (p *recordingProvider) Stream(ctx context.Context, req ai.StreamRequest) (*ai.EventStream, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	n := len(p.requests)
	p.mu.Unlock()
	if p.blockFrom > 0 && n >= p.blockFrom {
		if p.cancelOnBlock != nil {
			p.cancelOnBlock()
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return p.inner.Stream(ctx, req)
}

func toolNames(req ai.StreamRequest) []string {
	var names []string
	for _, tool := range req.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func newTaskTestAgent(p *recordingProvider, confirm ConfirmFunc, tools ...Tool) (*Agent, *TaskTool) {
	registry := providers.NewRegistry()
	registry.Register(p)
	model := ai.Model{ID: "t", Name: "t", Provider: "mock_test"}
	task := NewTaskTool(TaskToolOptions{Model: model, Registry: registry, Tools: tools, ConfirmFunc: confirm})
	task.SetSystemPrompt("base prompt")
	all := append(append([]Tool(nil), tools...), task)
	return New(Options{Model: model, Registry: registry, System: "base prompt", Tools: all, MaxTurns: 5, ConfirmFunc: confirm}), task
}

func TestTaskToolIsolatesSubagentContext(t *testing.T) {
	p := &recordingProvider{inner: &mockTestProvider{responses: []mockTestResponse{
		{toolCalls: []ai.ToolCall{{ID: "p1", Name: "task", Args: `{"description":"find usages","prompt":"list callers of Foo"}`}}, stop: ai.StopReasonToolUse},
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "safe", Args: `{}`}}, stop: ai.StopReasonToolUse},
		{text: "Foo is called from a.go:12", stop: ai.StopReasonStop},
		{text: "done", stop: ai.StopReasonStop},
	}}}
	safe := &concurrentSafeTool{}
	parent, _ := newTaskTestAgent(p, nil, safe)

	_, err := parent.Prompt(context.Background(), ai.NewTextUserMessage("parent secret question"))
	require.NoError(t, err)
	require.Len(t, p.requests, 4)

	child := p.requests[1]
	require.Len(t, child.Messages, 1, "subagent starts with only its prompt")
	assert.Contains(t, child.Messages[0].(ai.UserMessage).Content[0].Text, "list callers of Foo")
	assert.Contains(t, child.System, "base prompt")
	assert.Contains(t, child.System, "Subagent mode")
	assert.NotContains(t, toolNames(child), "task", "subagents cannot spawn subagents")

	final := p.requests[3]
	var sawChildCall bool
	var result string
	for _, msg := range final.Messages {
		switch m := msg.(type) {
		case ai.AssistantMessage:
			for _, call := range m.ToolCalls {
				sawChildCall = sawChildCall || call.ID == "c1"
			}
		case ai.ToolResultMessage:
			if m.ToolCallID == "p1" {
				result = m.Content
			}
		}
	}
	assert.False(t, sawChildCall, "subagent tool calls stay out of the parent history")
	assert.Contains(t, result, "Foo is called from a.go:12")
	assert.Contains(t, result, "1 tool calls")
	assert.Equal(t, int32(1), safe.executed.Load())
}

func TestTaskToolReadonlyLimitsTools(t *testing.T) {
	p := &recordingProvider{inner: &mockTestProvider{responses: []mockTestResponse{{text: "ok", stop: ai.StopReasonStop}}}}
	_, task := newTaskTestAgent(p, nil, &concurrentSafeTool{}, &concurrentDangerTool{})

	_, err := task.Execute(context.Background(), json.RawMessage(`{"description":"look","prompt":"look around","readonly":true}`), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"safe"}, toolNames(p.requests[0]))
	assert.True(t, task.IsConcurrencySafe(json.RawMessage(`{"prompt":"x","readonly":true}`)))
	assert.False(t, task.IsConcurrencySafe(json.RawMessage(`{"prompt":"x"}`)))
}

func TestTaskToolUsesParentApprovalWithLabel(t *testing.T) {
	p := &recordingProvider{inner: &mockTestProvider{responses: []mockTestResponse{
		{toolCalls: []ai.ToolCall{{ID: "c1", Name: "danger", Args: `{}`}}, stop: ai.StopReasonToolUse},
		{text: "refused", stop: ai.StopReasonStop},
	}}}
	var descriptions []string
	confirm := func(_ context.Context, req ConfirmationRequest) ConfirmDecision {
		descriptions = append(descriptions, req.Description)
		return ConfirmDecision{Approved: false, Reason: "no"}
	}
	danger := &concurrentDangerTool{}
	_, task := newTaskTestAgent(p, confirm, danger)

	_, err := task.Execute(context.Background(), json.RawMessage(`{"description":"cleanup","prompt":"delete things"}`), nil)
	require.NoError(t, err)
	require.Len(t, descriptions, 1)
	assert.True(t, strings.HasPrefix(descriptions[0], "[subagent: cleanup] "))
	assert.Equal(t, int32(0), danger.executed.Load(), "rejected approval is not bypassed")
}

func TestTaskToolCancelsWithParent(t *testing.T) {
	p := &recordingProvider{inner: &mockTestProvider{}, blockFrom: 1}
	_, task := newTaskTestAgent(p, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ToolResult, 1)
	go func() {
		result, _ := task.Execute(ctx, json.RawMessage(`{"description":"slow","prompt":"wait"}`), nil)
		done <- result
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case result := <-done:
		assert.True(t, result.IsError)
	case <-time.After(5 * time.Second):
		t.Fatal("subagent did not stop after cancellation")
	}
}

func TestTaskToolRequiresPrompt(t *testing.T) {
	_, err := NewTaskTool(TaskToolOptions{}).Validate(json.RawMessage(`{"description":"x"}`))
	assert.Error(t, err)
}

func TestTaskPartialFailureUsage(t *testing.T) {
	p := &recordingProvider{inner: &mockTestProvider{responses: []mockTestResponse{
		{text: "partial", stop: ai.StopReasonError, usage: ai.Usage{InputTokens: 19, OutputTokens: 2}, err: errors.New("stream failed")},
	}}}
	_, task := newTaskTestAgent(p, nil)
	result, err := task.Execute(context.Background(), json.RawMessage(`{"prompt":"inspect"}`), nil)
	require.Error(t, err)
	require.Equal(t, ai.Usage{InputTokens: 19, OutputTokens: 2}, result.SubagentUsage.Usage)
}
