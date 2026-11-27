package runtime

import (
	"context"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/extensions"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outputLimitProvider 记录每次模型请求携带的输出上限。
type outputLimitProvider struct{ limits []*int }

func (p *outputLimitProvider) Name() string { return "mock_output_limit" }

func (p *outputLimitProvider) Stream(ctx context.Context, req ai.StreamRequest) (*ai.EventStream, error) {
	p.limits = append(p.limits, req.MaxTokens)
	stream := ai.NewEventStream(8)
	partial := ai.StreamAssistantMessage{Text: "ok", StopReason: ai.StopReasonStop}
	go func() {
		defer stream.Close()
		_ = stream.Push(ctx, ai.EventStart{Partial: partial})
		_ = stream.Push(ctx, ai.EventTextStart{ContentIndex: 0, Partial: partial})
		_ = stream.Push(ctx, ai.EventTextDelta{ContentIndex: 0, Delta: "ok", Partial: partial})
		_ = stream.Push(ctx, ai.EventTextEnd{ContentIndex: 0, Text: "ok", Partial: partial})
		_ = stream.Push(ctx, ai.EventDone{Reason: ai.StopReasonStop, Message: partial})
		stream.SetResult(partial, nil)
	}()
	return stream, nil
}

func (p *outputLimitProvider) StreamSimple(ctx context.Context, req ai.SimpleStreamRequest) (*ai.EventStream, error) {
	return p.Stream(ctx, ai.StreamRequest{Model: req.Model, Messages: req.Messages, System: req.System, Tools: req.Tools})
}

func requestedOutputLimit(t *testing.T, modelID string, override int) *int {
	t.Helper()
	cfg := config.Default()
	cfg.Provider = "mock_output_limit"
	cfg.AnthropicModel = modelID
	cfg.Workspace = t.TempDir()
	cfg.SkillMarketEnabled = false
	cfg.MaxOutputTokens = override
	p := &outputLimitProvider{}
	registry := providers.NewRegistry()
	registry.Register(p)
	s, err := NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg}, Dependencies{
		SessionMgr: sessionmgr.NewManager(t.TempDir()), Registry: registry,
		ExtRegistry: extensions.NewRegistry(), Application: &confirmationTestApplication{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.Prompt(context.Background(), "hi")
	require.NoError(t, err)
	require.Len(t, p.limits, 1)
	return p.limits[0]
}

func TestSessionModelCarriesConfiguredOutputLimit(t *testing.T) {
	value := func(v int) *int { return &v }

	// 网关链名不在模型表里：默认显式下发 4096，配置后换成配置值。
	assert.Equal(t, value(4096), requestedOutputLimit(t, "coding", 0))
	require.Equal(t, value(30000), requestedOutputLimit(t, "coding", 30000))

	// 模型表里的条目默认带表里的值，配置可以覆盖它。
	assert.Equal(t, value(32768), requestedOutputLimit(t, "glm-5.3-flash", 0))
	assert.Equal(t, value(65536), requestedOutputLimit(t, "glm-5.3-flash", 65536))
}

func TestModelMaxOutputPrefersDeploymentOverride(t *testing.T) {
	assert.Equal(t, 4096, modelMaxOutput(config.Config{}, "coding"))
	assert.Equal(t, 32768, modelMaxOutput(config.Config{}, "glm-5.3"))
	assert.Equal(t, 65536, modelMaxOutput(config.Config{MaxOutputTokens: 65536}, "glm-5.3"))
}
