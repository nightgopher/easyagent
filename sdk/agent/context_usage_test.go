package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/compaction"
	"github.com/nightgopher/easyagent/sdk/session"
	"github.com/stretchr/testify/require"
)

type gatedThinkingProvider struct{ finish chan struct{} }

func (*gatedThinkingProvider) Name() string { return "gated-thinking" }
func (p *gatedThinkingProvider) StreamSimple(ctx context.Context, req ai.SimpleStreamRequest) (*ai.EventStream, error) {
	return p.Stream(ctx, ai.StreamRequest{Model: req.Model, Messages: req.Messages})
}

func TestOpenAIUnsentThinkingDoesNotTriggerCompaction(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	ctx := context.Background()
	storage := session.NewJSONLStorage(filepath.Join(t.TempDir(), "session.jsonl"))
	require.NoError(t, storage.Init())
	defer storage.Close()
	sess := session.New(storage)
	require.NoError(t, sess.AppendMessage(ctx, ai.NewTextUserMessage("implement task")))
	require.NoError(t, sess.AppendMessage(ctx, ai.AssistantMessage{Text: "planning", Thinking: "REASONING_MARKER_" + strings.Repeat("x", 300000)}))
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("read-%d", i)
		require.NoError(t, sess.AppendMessage(ctx, ai.AssistantMessage{ToolCalls: []ai.ToolCall{{ID: id, Name: "read", Args: `{}`}}}))
		size := 100
		if i < 2 {
			size = 10000
		}
		require.NoError(t, sess.AppendMessage(ctx, ai.ToolResultMessage{ToolCallID: id, Content: fmt.Sprintf("SOURCE_%d_", i) + strings.Repeat("y", size)}))
	}
	reg := providers.NewRegistry()
	reg.Register(providers.NewOpenAIProvider("test-key", server.URL))
	ag := New(Options{Registry: reg, Model: ai.Model{ID: "test", Provider: "openai", ContextWindow: 128000}, Session: sess, CompactionSettings: compaction.DefaultSettings()})
	usage, err := ag.ContextUsage(ctx)
	require.NoError(t, err)
	require.Less(t, usage.EstimatedTokens, 20000, "the estimate must exclude the unsent reasoning")
	micro := 0
	ag.Subscribe(func(_ context.Context, event AgentEvent) {
		if _, ok := event.(EventMicroCompacted); ok {
			micro++
		}
	})
	_, err = ag.Prompt(ctx, ai.NewTextUserMessage("continue"))
	require.NoError(t, err)
	require.Zero(t, micro, "reasoning omitted from the wire must not evict source")
	require.NotContains(t, string(body), "REASONING_MARKER_")
	require.Contains(t, string(body), "SOURCE_0_")
}
func (p *gatedThinkingProvider) Stream(ctx context.Context, req ai.StreamRequest) (*ai.EventStream, error) {
	stream := ai.NewEventStream(8)
	go func() {
		defer stream.Close()
		_ = stream.Push(ctx, ai.EventThinkingDelta{Delta: "检查输入后再给出答案"})
		select {
		case <-p.finish:
		case <-ctx.Done():
			return
		}
		msg := ai.StreamAssistantMessage{Thinking: "检查输入后再给出答案", Text: "答案", StopReason: ai.StopReasonStop, Usage: ai.Usage{InputTokens: 42, OutputTokens: 8}}
		_ = stream.Push(ctx, ai.EventTextDelta{Delta: msg.Text})
		_ = stream.Push(ctx, ai.EventDone{Message: msg, Reason: msg.StopReason})
		stream.SetResult(msg, nil)
	}()
	return stream, nil
}

func TestThinkingArrivesBeforeProviderFinishesAndSurvivesReload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	storage := session.NewJSONLStorage(path)
	require.NoError(t, storage.Init())
	p := &gatedThinkingProvider{finish: make(chan struct{})}
	registry := providers.NewRegistry()
	registry.Register(p)
	ag := New(Options{Registry: registry, Model: ai.Model{ID: "gpt-4o", Provider: p.Name(), ContextWindow: 128000}, Session: session.New(storage)})
	events, err := ag.PromptStream(ctx, ai.NewTextUserMessage("问题"))
	require.NoError(t, err)
	select {
	case e := <-events:
		require.Equal(t, StreamEventContextUsage, e.Type)
	case <-ctx.Done():
		t.Fatal("no context event")
	}
	select {
	case e := <-events:
		require.Equal(t, StreamEventThinkingDelta, e.Type)
		require.Equal(t, "检查输入后再给出答案", e.TextDelta)
	case <-ctx.Done():
		t.Fatal("thinking buffered until done")
	}
	// The provider is still blocked: the UI can display reasoning before a final answer exists.
	select {
	case e := <-events:
		t.Fatalf("unexpected event while provider gated: %v", e.Type)
	default:
	}
	close(p.finish)
	var duration int64
	var measured *ContextUsage
	var ended bool
	for e := range events {
		if e.Type == StreamEventThinkingEnd {
			duration = e.DurationMS
			ended = true
		}
		if e.Type == StreamEventContextUsage {
			measured = e.ContextUsage
		}
	}
	require.True(t, ended)
	require.NotNil(t, measured)
	require.Equal(t, 42, measured.LastRequest.InputTokens)
	require.NoError(t, storage.Close())
	restored := session.NewJSONLStorage(path)
	require.NoError(t, restored.Init())
	defer restored.Close()
	sess := session.New(restored)
	require.NoError(t, sess.InitFromStorage(ctx))
	history, err := sess.BuildContext(ctx)
	require.NoError(t, err)
	msg := history[len(history)-1].(ai.AssistantMessage)
	require.Equal(t, "检查输入后再给出答案", msg.Thinking)
	require.Equal(t, duration, msg.ThinkingDurationMS)
	require.Equal(t, 42, msg.Usage.InputTokens)
}

func TestContextIncludesSystemAndToolsAndKeepsLatestMeasurement(t *testing.T) {
	req := ai.StreamRequest{Model: ai.Model{ID: "unregistered", ContextWindow: 128000}, System: "system prompt", Tools: []ai.ToolDefinition{{Name: "echo", Description: "echo tool"}}, Messages: []ai.Message{ai.NewTextUserMessage("hello")}}
	usage := estimateContext(req, ai.Usage{InputTokens: 80, OutputTokens: 12})
	require.Positive(t, usage.System)
	require.Positive(t, usage.Tools)
	require.Positive(t, usage.Messages)
	require.Equal(t, usage.Messages+usage.System+usage.Tools, usage.EstimatedTokens)
	require.False(t, usage.WindowKnown)
	require.Equal(t, 128000, usage.ContextWindow)
	req.Model.ID = "gpt-4o"
	req.Model.ContextWindow = 0
	require.True(t, estimateContext(req, ai.Usage{}).WindowKnown)
	require.Equal(t, 128000, estimateContext(req, ai.Usage{}).ContextWindow)
}
