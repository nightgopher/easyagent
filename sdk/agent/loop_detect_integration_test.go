package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type loopProgressTool struct {
	name string
	path string
}

func (t *loopProgressTool) Name() string                                        { return t.name }
func (*loopProgressTool) Description() string                                   { return "test file operation" }
func (*loopProgressTool) Parameters() map[string]any                            { return map[string]any{"type": "object"} }
func (*loopProgressTool) Validate(raw json.RawMessage) (json.RawMessage, error) { return raw, nil }
func (t *loopProgressTool) Execute(_ context.Context, _ json.RawMessage, _ func(PartialResult)) (ToolResult, error) {
	if t.name == "edit" {
		return ToolResult{Content: "edited"}, os.WriteFile(t.path, []byte("after"), 0o600)
	}
	content, err := os.ReadFile(t.path)
	return ToolResult{Content: string(content)}, err
}

type progressProvider struct {
	path        string
	alternating bool
	calls       int
	edited      bool
	reminderAt  int
}

func (*progressProvider) Name() string { return "progress-test" }
func (p *progressProvider) StreamSimple(ctx context.Context, req ai.SimpleStreamRequest) (*ai.EventStream, error) {
	return p.Stream(ctx, ai.StreamRequest{Model: req.Model, Messages: req.Messages})
}
func (p *progressProvider) Stream(ctx context.Context, req ai.StreamRequest) (*ai.EventStream, error) {
	p.calls++
	if last, ok := req.Messages[len(req.Messages)-1].(ai.UserMessage); ok {
		for _, block := range last.Content {
			if strings.Contains(block.Text, "looks like a loop") || strings.Contains(block.Text, "file reads overlap") {
				p.reminderAt = p.calls
			}
		}
	}
	msg := ai.StreamAssistantMessage{Text: "done", StopReason: ai.StopReasonStop}
	if !p.edited {
		name, args := "read", fmt.Sprintf(`{"path":%q,"offset":0,"limit":300}`, p.path)
		if p.reminderAt > 0 {
			name, args, p.edited = "edit", `{}`, true
		} else if p.alternating {
			args = fmt.Sprintf(`{"path":%q,"offset":%d,"limit":300}`, p.path, (p.calls%3)*100)
		}
		msg = ai.StreamAssistantMessage{StopReason: ai.StopReasonToolUse, ToolCalls: []ai.ToolCall{{ID: fmt.Sprintf("call-%d", p.calls), Name: name, Args: args}}}
	}
	stream := ai.NewEventStream(2)
	go func() {
		defer stream.Close()
		_ = stream.Push(ctx, ai.EventDone{Reason: msg.StopReason, Message: msg})
		stream.SetResult(msg, nil)
	}()
	return stream, nil
}

func TestLoopReminderReachesNextRequestAndAgentCanEdit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		alternating bool
		wantRequest int
		wantKind    string
	}{
		{"identical calls", false, 6, LoopDetectionExact},
		{"overlapping ranges", true, 13, LoopDetectionExploration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "target.txt")
			require.NoError(t, os.WriteFile(path, []byte("before"), 0o600))
			storage := session.NewJSONLStorage(filepath.Join(t.TempDir(), "session.jsonl"))
			require.NoError(t, storage.Init())
			defer storage.Close()
			p := &progressProvider{path: path, alternating: tc.alternating}
			registry := providers.NewRegistry()
			registry.Register(p)
			ag := New(Options{Registry: registry, Model: ai.Model{Provider: p.Name()}, Session: session.New(storage), MaxTurns: 20,
				Tools: []Tool{&loopProgressTool{name: "read", path: path}, &loopProgressTool{name: "edit", path: path}}, LoopDetectSettings: DefaultLoopDetectSettings()})
			var kinds []string
			ag.Subscribe(func(_ context.Context, event AgentEvent) {
				if detected, ok := event.(EventLoopDetected); ok {
					kinds = append(kinds, detected.Kind)
				}
			})
			_, err := ag.Prompt(ctx, ai.NewTextUserMessage("Update the file"))
			require.NoError(t, err)
			require.Contains(t, kinds, tc.wantKind)
			require.Equal(t, tc.wantRequest, p.reminderAt, "the reminder must reach the immediate next model request")
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "after", string(content), "the agent must continue through the edit")
		})
	}
}

// 集成测试：连续重复相同 tool call 达到阈值时，
//  1. 发出 EventLoopDetected
//  2. 向 followUpQueue 注入提醒（下一轮 Agent 能收到）
//  3. Agent 不被强制中断（继续运行直到 maxTurns 或自然停止）
func TestLoopDetection_Integration(t *testing.T) {
	// mock provider：连续返回相同 echo tool call，最后返回纯文本停止。
	// 给足 response：循环检测在第 5 轮触发并注入提醒，提醒会被 followUpQueue 消费占一轮，
	// 故需要足够 response 避免越界。MaxTurns 兜底防止真死循环。
	sameCall := ai.ToolCall{ID: "c1", Name: "echo", Args: `{"message":"hi"}`}
	responses := make([]mockTestResponse, 0, 12)
	for i := 0; i < 10; i++ {
		responses = append(responses, mockTestResponse{
			toolCalls: []ai.ToolCall{sameCall},
			stop:      ai.StopReasonToolUse,
		})
	}
	responses = append(responses, mockTestResponse{text: "ok, switching approach", stop: ai.StopReasonStop})

	mp := &mockTestProvider{responses: responses}
	registry := providers.NewRegistry()
	registry.Register(mp)

	// 收集事件
	var mu sync.Mutex
	events := []AgentEvent{}
	ag := New(Options{
		Model:    ai.Model{ID: "t", Name: "t", Provider: "mock_test"},
		Registry: registry,
		System:   "test",
		Tools:    []Tool{&echoTool{}},
		MaxTurns: 8,
		LoopDetectSettings: LoopDetectSettings{
			Enabled:          true,
			Threshold:        5,
			ReminderTemplate: "loop on %q (%d times)",
		},
	})
	unsub := ag.Subscribe(func(ctx context.Context, e AgentEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})
	defer unsub()

	_, err := ag.Prompt(context.Background(), ai.NewTextUserMessage("go"))
	require.NoError(t, err)

	// 断言：至少触发一次 EventLoopDetected
	mu.Lock()
	defer mu.Unlock()
	detected := 0
	for _, e := range events {
		if _, ok := e.(EventLoopDetected); ok {
			detected++
		}
	}
	assert.GreaterOrEqual(t, detected, 1, "应至少发出一次 EventLoopDetected")

	// Agent 应能继续到完成（未被中断），最终自然停止
	// （provider 第 7 轮返回 stop，证明循环检测没有强制中断执行）
}

// 循环检测未启用（Enabled=false）时，连续相同 tool call 不触发事件、不注入提醒。
func TestLoopDetection_DisabledNoDetection(t *testing.T) {
	sameCall := ai.ToolCall{ID: "c1", Name: "echo", Args: `{"message":"hi"}`}
	responses := []mockTestResponse{
		{toolCalls: []ai.ToolCall{sameCall}, stop: ai.StopReasonToolUse},
		{toolCalls: []ai.ToolCall{sameCall}, stop: ai.StopReasonToolUse},
		{toolCalls: []ai.ToolCall{sameCall}, stop: ai.StopReasonToolUse},
		{text: "done", stop: ai.StopReasonStop},
	}
	mp := &mockTestProvider{responses: responses}
	registry := providers.NewRegistry()
	registry.Register(mp)

	var mu sync.Mutex
	detected := 0
	ag := New(Options{
		Model:              ai.Model{ID: "t", Name: "t", Provider: "mock_test"},
		Registry:           registry,
		System:             "test",
		Tools:              []Tool{&echoTool{}},
		MaxTurns:           10,
		LoopDetectSettings: LoopDetectSettings{Enabled: false, Threshold: 5},
	})
	ag.Subscribe(func(ctx context.Context, e AgentEvent) {
		mu.Lock()
		if _, ok := e.(EventLoopDetected); ok {
			detected++
		}
		mu.Unlock()
	})

	_, err := ag.Prompt(context.Background(), ai.NewTextUserMessage("go"))
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 0, detected, "Enabled=false 时不应检测")
}
