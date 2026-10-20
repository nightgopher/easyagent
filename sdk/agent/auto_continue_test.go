package agent

import (
	"context"
	"testing"

	"github.com/hwj123hwj/easyagent/sdk/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockContinueProvider struct {
	responses []ai.StreamAssistantMessage
	requests  []ai.StreamRequest
}

func (m *mockContinueProvider) Stream(ctx context.Context, req ai.StreamRequest) (*ai.EventStream, error) {
	m.requests = append(m.requests, req)
	stream := ai.NewEventStream(8)
	idx := len(m.requests) - 1
	resp := m.responses[idx]

	go func() {
		defer stream.Close()
		_ = stream.Push(ctx, ai.EventStart{Partial: resp})
		if resp.Text != "" {
			_ = stream.Push(ctx, ai.EventTextStart{ContentIndex: 0, Partial: resp})
			_ = stream.Push(ctx, ai.EventTextDelta{ContentIndex: 0, Delta: resp.Text, Partial: resp})
			_ = stream.Push(ctx, ai.EventTextEnd{ContentIndex: 0, Text: resp.Text, Partial: resp})
		}
		_ = stream.Push(ctx, ai.EventDone{Reason: resp.StopReason, Message: resp})
		stream.SetResult(resp, nil)
	}()

	return stream, nil
}

func TestAutoContinue_SmallMaxOutputLengthSignal(t *testing.T) {
	t.Setenv("EA_AUTO_CONTINUE", "true")

	provider := &mockContinueProvider{
		responses: []ai.StreamAssistantMessage{
			{
				Text:       "前半部分长回复内容...",
				StopReason: ai.StopReasonLength,
				Usage:      ai.Usage{OutputTokens: 100},
			},
			{
				Text:       "后半部分完成内容。",
				StopReason: ai.StopReasonStop,
				Usage:      ai.Usage{OutputTokens: 50},
			},
		},
	}

	model := ai.Model{ID: "test-model", MaxTokens: 100}
	a := New(Options{
		Model: model,
	})

	consume := func(stream *ai.EventStream) (ai.StreamAssistantMessage, error) {
		for range stream.Events() {
		}
		return stream.Result()
	}

	history := []ai.Message{ai.NewTextUserMessage("请写一篇长文章")}
	result, _, err := processTurn(context.Background(), a, provider, consume, history, nil)
	require.NoError(t, err)

	// 验证发起了 2 次请求（第 1 次主请求 + 第 2 次自动续写）
	assert.Len(t, provider.requests, 2)
	assert.Contains(t, result.assistant.Text, "前半部分长回复内容...")
	assert.Contains(t, result.assistant.Text, "后半部分完成内容。")
	assert.Contains(t, result.assistant.Text, "[已自动续写]")
}

func TestAutoContinue_GatewayMissingFinishReason(t *testing.T) {
	t.Setenv("EA_AUTO_CONTINUE", "true")

	// 模拟网关吞掉 finish_reason（缺失），但输出 tokens 逼近上限 (95/100 >= 95%)
	provider := &mockContinueProvider{
		responses: []ai.StreamAssistantMessage{
			{
				Text:              "网关截断输出内容逼近上限...",
				StopReason:        ai.StopReasonStop,
				StopReasonMissing: true,
				Usage:             ai.Usage{OutputTokens: 96},
			},
			{
				Text:       "续写补全内容。",
				StopReason: ai.StopReasonStop,
				Usage:      ai.Usage{OutputTokens: 30},
			},
		},
	}

	model := ai.Model{ID: "test-model", MaxTokens: 100}
	a := New(Options{
		Model: model,
	})

	consume := func(stream *ai.EventStream) (ai.StreamAssistantMessage, error) {
		for range stream.Events() {
		}
		return stream.Result()
	}

	history := []ai.Message{ai.NewTextUserMessage("长回复测试")}
	result, _, err := processTurn(context.Background(), a, provider, consume, history, nil)
	require.NoError(t, err)

	// 命中疑似截断并发生续写
	assert.Len(t, provider.requests, 2)
	assert.Contains(t, result.assistant.Text, "网关截断输出内容逼近上限...")
	assert.Contains(t, result.assistant.Text, "续写补全内容。")
	assert.Contains(t, result.assistant.Text, "[已自动续写]")
}

func TestAutoContinue_DisabledByEnv(t *testing.T) {
	t.Setenv("EA_AUTO_CONTINUE", "false")

	provider := &mockContinueProvider{
		responses: []ai.StreamAssistantMessage{
			{
				Text:              "网关截断输出内容...",
				StopReason:        ai.StopReasonStop,
				StopReasonMissing: true,
				Usage:             ai.Usage{OutputTokens: 98},
			},
		},
	}

	model := ai.Model{ID: "test-model", MaxTokens: 100}
	a := New(Options{
		Model: model,
	})

	consume := func(stream *ai.EventStream) (ai.StreamAssistantMessage, error) {
		for range stream.Events() {
		}
		return stream.Result()
	}

	history := []ai.Message{ai.NewTextUserMessage("长回复测试")}
	result, _, err := processTurn(context.Background(), a, provider, consume, history, nil)
	require.NoError(t, err)

	// 关闭开关后只请求 1 次，不续写，且命中疑似截断路径包含标记
	assert.Len(t, provider.requests, 1)
	assert.Contains(t, result.assistant.Text, "网关截断输出内容...")
	assert.Contains(t, result.assistant.Text, "[回复可能被截断]")
	assert.NotContains(t, result.assistant.Text, "[已自动续写]")
}
