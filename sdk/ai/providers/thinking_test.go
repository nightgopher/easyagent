package providers

import (
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/stretchr/testify/assert"
)

func openaiThinkingRequest(t *testing.T, options ThinkingOptions, override *ai.ThinkingOverride) openAIRequest {
	t.Helper()
	p := NewOpenAIProvider("test-key", "http://localhost", options)
	req := ai.StreamRequest{Model: ai.Model{ID: "glm-5.3-flash"}, Messages: []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{{Type: "text", Text: "hi"}}}}, Thinking: override}
	return p.buildOpenAIRequest(req, false)
}

func TestOpenAIThinkingNilKeepsConstructorOptions(t *testing.T) {
	wire := openaiThinkingRequest(t, ThinkingOptions{Mode: "enabled", Effort: "low"}, nil)
	assert.NotNil(t, wire.Thinking)
	assert.Equal(t, "enabled", wire.Thinking.Type)
	assert.Equal(t, "low", wire.ReasoningEffort)
	assert.Zero(t, wire.Temperature)
}

func TestOpenAIThinkingOverrideReplacesConstructorOptions(t *testing.T) {
	wire := openaiThinkingRequest(t, ThinkingOptions{Mode: "enabled", Effort: "low"}, &ai.ThinkingOverride{Effort: "high"})
	assert.Nil(t, wire.Thinking, "override without mode must not leak the constructor's thinking field")
	assert.Equal(t, "high", wire.ReasoningEffort)
}

func TestOpenAIThinkingOverrideDisabled(t *testing.T) {
	wire := openaiThinkingRequest(t, ThinkingOptions{Mode: "enabled", Effort: "low"}, &ai.ThinkingOverride{Mode: "disabled"})
	assert.NotNil(t, wire.Thinking)
	assert.Equal(t, "disabled", wire.Thinking.Type)
	assert.Equal(t, "", wire.ReasoningEffort)
}

func TestOpenAIThinkingUnsetEverywhere(t *testing.T) {
	wire := openaiThinkingRequest(t, ThinkingOptions{}, nil)
	assert.Nil(t, wire.Thinking)
	assert.Equal(t, "", wire.ReasoningEffort)
	assert.InDelta(t, 0.7, wire.Temperature, 0.0001)
}

func anthropicThinkingRequest(options ThinkingOptions, override *ai.ThinkingOverride) anthropicRequest {
	p := NewAnthropicProvider("test-key", "http://localhost", options)
	req := ai.StreamRequest{Model: ai.Model{ID: "claude-test"}, Messages: []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{{Type: "text", Text: "hi"}}}}, Thinking: override}
	return p.buildRequest(req, false)
}

func TestAnthropicThinkingNilKeepsConstructorOptions(t *testing.T) {
	wire := anthropicThinkingRequest(ThinkingOptions{Mode: "enabled", BudgetTokens: 4096}, nil)
	assert.NotNil(t, wire.Thinking)
	assert.Equal(t, "enabled", wire.Thinking.Type)
	assert.Equal(t, 4096, wire.Thinking.BudgetTokens)
}

func TestAnthropicThinkingEffortMapsToBudget(t *testing.T) {
	for _, tc := range []struct {
		effort string
		budget int
	}{{"low", 2048}, {"medium", 8192}, {"high", 16384}} {
		wire := anthropicThinkingRequest(ThinkingOptions{}, &ai.ThinkingOverride{Effort: tc.effort})
		assert.NotNil(t, wire.Thinking, tc.effort)
		assert.Equal(t, "enabled", wire.Thinking.Type, tc.effort)
		assert.Equal(t, tc.budget, wire.Thinking.BudgetTokens, tc.effort)
		assert.Greater(t, wire.MaxTokens, wire.Thinking.BudgetTokens, tc.effort)
	}
}

func TestAnthropicThinkingOverrideDisabled(t *testing.T) {
	wire := anthropicThinkingRequest(ThinkingOptions{Mode: "enabled", BudgetTokens: 4096}, &ai.ThinkingOverride{Mode: "disabled"})
	assert.NotNil(t, wire.Thinking)
	assert.Equal(t, "disabled", wire.Thinking.Type)
}

func TestCodexEffortFor(t *testing.T) {
	req := ai.StreamRequest{}
	assert.Equal(t, "medium", codexEffortFor(req, ThinkingOptions{}), "constructor default")
	assert.Equal(t, "high", codexEffortFor(req, ThinkingOptions{Effort: "high"}), "constructor effort")

	override := &ai.ThinkingOverride{Effort: "low"}
	assert.Equal(t, "low", codexEffortFor(ai.StreamRequest{Thinking: override}, ThinkingOptions{Effort: "high"}), "override wins")

	off := &ai.ThinkingOverride{Mode: "disabled"}
	assert.Equal(t, "minimal", codexEffortFor(ai.StreamRequest{Thinking: off}, ThinkingOptions{Effort: "high"}), "off falls to minimal")

	bare := &ai.ThinkingOverride{Mode: "enabled"}
	assert.Equal(t, "minimal", codexEffortFor(ai.StreamRequest{Thinking: bare}, ThinkingOptions{}), "override without effort falls to minimal")
}
