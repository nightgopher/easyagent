package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/stretchr/testify/require"
)

func TestIdleZeroBytesRetriesOnceButPartialBytesNeverRetry(t *testing.T) {
	t.Setenv("EA_STREAM_IDLE_TIMEOUT", "40ms")
	old := streamRetryDelay
	streamRetryDelay = time.Millisecond
	t.Cleanup(func() { streamRetryDelay = old })
	for _, kind := range []string{"openai", "anthropic"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprint(kind, partial), func(t *testing.T) {
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					if partial {
						fmt.Fprint(w, "data: incomplete-without-newline")
					}
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				}))
				defer srv.Close()
				var p Provider = NewOpenAIProvider("test", srv.URL)
				if kind == "anthropic" {
					p = NewAnthropicProvider("test", srv.URL)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				stream, err := p.Stream(ctx, ai.StreamRequest{})
				require.NoError(t, err)
				for range stream.Events() {
				}
				_, err = stream.Result()
				require.ErrorContains(t, err, "stream idle over 40ms")
				require.ErrorContains(t, err, "bytes")
				want := int32(2)
				if partial {
					want = 1
				}
				require.Equal(t, want, calls.Load())
				require.NoError(t, ctx.Err())
			})
		}
	}
}
func TestIdleByteProgressWithoutNewlineKeepsStreamAlive(t *testing.T) {
	t.Setenv("EA_STREAM_IDLE_TIMEOUT", "100ms")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, piece := range []string{`data: {"choices":[`, `{"index":0,"delta":`, `{"content":"ok"}}]}`} {
			fmt.Fprint(w, piece)
			w.(http.Flusher).Flush()
			time.Sleep(40 * time.Millisecond)
		}
		fmt.Fprint(w, "\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	_, msg, err := collectStream(t, NewOpenAIProvider("test", srv.URL))
	require.NoError(t, err)
	require.Equal(t, "ok", msg.Text)
}
func TestThinkingRequestOptionsAndSignedBlocks(t *testing.T) {
	options := ThinkingOptions{Mode: "enabled", BudgetTokens: 2048, Effort: "high"}
	req := ai.StreamRequest{Model: ai.Model{ID: "glm-5.3"}}
	o := NewOpenAIProvider("test", "", options).buildOpenAIRequest(req, true)
	require.Equal(t, "enabled", o.Thinking.Type)
	require.Equal(t, "high", o.ReasoningEffort)
	plain := NewOpenAIProvider("test", "").buildOpenAIRequest(req, true)
	require.Nil(t, plain.Thinking)
	require.Empty(t, plain.ReasoningEffort)
	a := NewAnthropicProvider("test", "", options).buildRequest(req, true)
	require.Equal(t, 2048, a.Thinking.BudgetTokens)
	require.Greater(t, a.MaxTokens, a.Thinking.BudgetTokens)
	lines := []string{`event: message_start`, `data: {"message":{}}`, `event: content_block_start`, `data: {"index":0,"content_block":{"type":"thinking","thinking":""}}`, `event: content_block_delta`, `data: {"index":0,"delta":{"type":"thinking_delta","thinking":"reason"}}`, `event: content_block_delta`, `data: {"index":0,"delta":{"type":"signature_delta","signature":"signed"}}`, `event: content_block_stop`, `data: {"index":0}`, `event: content_block_start`, `data: {"index":1,"content_block":{"type":"tool_use","id":"call","name":"read"}}`, `event: content_block_delta`, `data: {"index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`, `event: content_block_stop`, `data: {"index":1}`, `event: message_delta`, `data: {"delta":{"stop_reason":"tool_use"}}`, `event: message_stop`, `data: {}`}
	srv := anthropicSSEServer(t, lines...)
	stream, err := NewAnthropicProvider("test", srv.URL, options).Stream(context.Background(), req)
	require.NoError(t, err)
	for range stream.Events() {
	}
	msg, err := stream.Result()
	require.NoError(t, err)
	require.Equal(t, "reason", msg.Thinking)
	require.Equal(t, "signed", msg.ThinkingBlocks[0].Signature)
	data, err := json.Marshal(ai.AssistantMessage{ThinkingBlocks: msg.ThinkingBlocks, ToolCalls: msg.ToolCalls})
	require.NoError(t, err)
	var restored ai.AssistantMessage
	require.NoError(t, json.Unmarshal(data, &restored))
	roundtrip := convertToAnthropicMessages([]ai.Message{restored, ai.ToolResultMessage{ToolCallID: "call", Content: "done"}})
	require.Equal(t, "thinking", roundtrip[0].Content[0].Type)
	require.Equal(t, "signed", roundtrip[0].Content[0].Signature)
}
