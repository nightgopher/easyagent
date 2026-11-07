package agent

import (
	"context"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestSteeringIsDeliveredDuringActiveRun(t *testing.T) {
	for _, tc := range []struct {
		name       string
		duringTool bool
	}{
		{"during tool execution", true},
		{"during final response", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			responses := []mockTestResponse{{text: "initial answer", stop: ai.StopReasonStop}, {text: "corrected", stop: ai.StopReasonStop}}
			if tc.duringTool {
				responses[0] = mockTestResponse{toolCalls: []ai.ToolCall{{ID: "c1", Name: "echo", Args: `{"message":"hi"}`}}, stop: ai.StopReasonToolUse}
			}
			provider := &capturingProvider{inner: &mockTestProvider{responses: responses}}
			registry := providers.NewRegistry()
			registry.Register(provider)
			ag := New(Options{Registry: registry, Model: ai.Model{Provider: provider.Name()}, Tools: []Tool{&echoTool{}}, MaxTurns: 5})
			var requests [][]ai.Message
			provider.onMessages = func(messages []ai.Message) {
				requests = append(requests, append([]ai.Message(nil), messages...))
				if !tc.duringTool && len(requests) == 1 {
					ag.Steer(ctx, ai.NewTextUserMessage("user correction"))
				}
			}
			if tc.duringTool {
				ag.Subscribe(func(_ context.Context, event AgentEvent) {
					if _, ok := event.(EventToolExecutionStart); ok {
						ag.Steer(ctx, ai.NewTextUserMessage("user correction"))
					}
				})
			}
			result, err := ag.Prompt(ctx, ai.NewTextUserMessage("original request"))
			require.NoError(t, err)
			require.Equal(t, "corrected", result.Text)
			require.Len(t, requests, 2)
			last := requests[1][len(requests[1])-1].(ai.UserMessage)
			require.Equal(t, "user correction", last.Content[0].Text)
			if tc.duringTool {
				_, ok := requests[1][len(requests[1])-2].(ai.ToolResultMessage)
				require.True(t, ok, "steering must follow the completed tool result")
			}
		})
	}
}
