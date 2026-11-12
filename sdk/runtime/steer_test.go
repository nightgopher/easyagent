package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/extensions"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
	"github.com/stretchr/testify/require"
)

// steerBlockingProvider 第一次 Stream 阻塞到 release 被关闭（保证 agent 处于
// StateRunning），并记录每次请求的完整消息列表，供插话断言使用。
type steerBlockingProvider struct {
	mu       sync.Mutex
	requests [][]ai.Message
	release  chan struct{}
}

func (p *steerBlockingProvider) Name() string { return "mock_steer" }

func (p *steerBlockingProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *steerBlockingProvider) Stream(ctx context.Context, req ai.StreamRequest) (*ai.EventStream, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req.Messages)
	call := len(p.requests)
	p.mu.Unlock()
	stream := ai.NewEventStream(8)
	go func() {
		defer stream.Close()
		select {
		case <-p.release:
		case <-ctx.Done():
			stream.SetResult(ai.StreamAssistantMessage{}, ctx.Err())
			return
		}
		text := "done"
		if call > 1 {
			text = "handled steering"
		}
		partial := ai.StreamAssistantMessage{Text: text, StopReason: ai.StopReasonStop}
		_ = stream.Push(ctx, ai.EventStart{Partial: partial})
		_ = stream.Push(ctx, ai.EventTextStart{ContentIndex: 0, Partial: partial})
		_ = stream.Push(ctx, ai.EventTextDelta{ContentIndex: 0, Delta: text, Partial: partial})
		_ = stream.Push(ctx, ai.EventTextEnd{ContentIndex: 0, Text: text, Partial: partial})
		_ = stream.Push(ctx, ai.EventDone{Reason: ai.StopReasonStop, Message: partial})
		stream.SetResult(partial, nil)
	}()
	return stream, nil
}

func (p *steerBlockingProvider) StreamSimple(ctx context.Context, req ai.SimpleStreamRequest) (*ai.EventStream, error) {
	return p.Stream(ctx, ai.StreamRequest{
		Model:    req.Model,
		Messages: req.Messages,
		System:   req.System,
		Tools:    req.Tools,
	})
}

func steerUserText(msg ai.Message) string {
	if u, ok := msg.(ai.UserMessage); ok {
		var sb strings.Builder
		for _, block := range u.Content {
			sb.WriteString(block.Text)
		}
		return sb.String()
	}
	return ""
}

func newSteerTestSession(t *testing.T) (*AgentSession, *steerBlockingProvider) {
	t.Helper()
	ctx := context.Background()
	manager := sessionmgr.NewManager(t.TempDir())
	cfg := config.Default()
	cfg.Provider = "mock_steer"
	cfg.AnthropicModel = "test-model"
	cfg.Workspace = t.TempDir()
	cfg.SkillMarketEnabled = false
	registry := providers.NewRegistry()
	bp := &steerBlockingProvider{release: make(chan struct{})}
	registry.Register(bp)
	s, err := NewAgentSession(ctx, AgentSessionOptions{Config: cfg}, Dependencies{
		SessionMgr: manager, Registry: registry, ExtRegistry: extensions.NewRegistry(), Application: &confirmationTestApplication{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, bp
}

// 空闲时 Steer 返回 ErrAgentNotRunning；运行中注入的消息在下一个请求边界
// 进入模型请求（作为最后一条 user message），不取消也不打断原任务。
func TestAgentSessionSteerWhileRunning(t *testing.T) {
	ctx := context.Background()
	s, bp := newSteerTestSession(t)

	// 空闲：拒绝插话并给出可回退的信号。
	require.ErrorIs(t, s.Steer(ctx, ai.NewTextUserMessage("early")), agent.ErrAgentNotRunning)

	stream, err := s.PromptStream(ctx, "long task")
	require.NoError(t, err)

	// 等第一次模型请求已在 provider 侧阻塞：此时才是真正的「任务运行中」。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && bp.callCount() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, 1, bp.callCount(), "first model request should be in flight")

	// 运行中：注入成功，且原任务不被打断。
	require.NoError(t, s.Steer(ctx, ai.NewTextUserMessage("also do X")))
	require.Equal(t, agent.StateRunning, s.Agent().State())

	close(bp.release) // 放行第一次请求，循环在下一个边界取走插话再请求一次。
	for range stream {
	}

	require.Equal(t, agent.StateIdle, s.Agent().State(), "run should finish idle")
	bp.mu.Lock()
	defer bp.mu.Unlock()
	require.Len(t, bp.requests, 2, "steering must trigger exactly one more model request")
	require.NotEmpty(t, bp.requests[1])
	last := bp.requests[1][len(bp.requests[1])-1]
	require.Equal(t, ai.RoleUser, last.Role())
	require.Contains(t, steerUserText(last), "also do X",
		"steered message must reach the next model request as the last user message")
}

// 插话按普通 user message 持久化，会话历史完整。
func TestAgentSessionSteerPersistsMessage(t *testing.T) {
	ctx := context.Background()
	s, bp := newSteerTestSession(t)

	stream, err := s.PromptStream(ctx, "long task")
	require.NoError(t, err)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && bp.callCount() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, 1, bp.callCount(), "first model request should be in flight")
	require.NoError(t, s.Steer(ctx, ai.NewTextUserMessage("persist me")))
	close(bp.release)
	for range stream {
	}

	messages, err := s.Session().BuildContext(ctx)
	require.NoError(t, err)
	found := false
	for _, msg := range messages {
		if msg.Role() == ai.RoleUser && strings.Contains(steerUserText(msg), "persist me") {
			found = true
			break
		}
	}
	require.True(t, found, "steered message must be persisted as a user message")
}
