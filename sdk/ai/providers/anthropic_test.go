package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// anthropicSSEServer 按给定事件/data 行输出 Anthropic 格式 SSE。
func anthropicSSEServer(t *testing.T, lines ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprintf(w, "%s\n\n", l)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func anthropicCollect(t *testing.T, p *AnthropicProvider) (ai.StreamAssistantMessage, error) {
	t.Helper()
	stream, err := p.Stream(context.Background(), ai.StreamRequest{Model: ai.Model{ID: "test-model"}})
	require.NoError(t, err)
	for range stream.Events() {
	}
	msg, err := stream.Result()
	return msg, err
}

// TestAnthropicStreamThinkingAccumulation：Anthropic thinking 块累积进
// partial.Thinking（openai 侧的 reasoning_content 处理与此对齐）。
func TestAnthropicStreamThinkingAccumulation(t *testing.T) {
	srv := anthropicSSEServer(t,
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":10}}}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先想想。"}}`,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"答案是 42。"}}`,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
	)
	p := NewAnthropicProvider("test-key", srv.URL)

	msg, err := anthropicCollect(t, p)

	require.NoError(t, err)
	assert.Equal(t, "先想想。", msg.Thinking)
	assert.Equal(t, "答案是 42。", msg.Text)
	assert.Equal(t, ai.StopReasonStop, msg.StopReason)
}

// TestAnthropicStreamIdleTimeout：上游停滞超空闲阈值时报错，不伪装正常完成。
func TestAnthropicStreamIdleTimeout(t *testing.T) {
	old := streamIdleTimeout
	streamIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	p := NewAnthropicProvider("test-key", srv.URL)

	msg, err := anthropicCollect(t, p)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "idle")
	assert.Equal(t, ai.StopReasonError, msg.StopReason)
}

// ─── 回归：断流重试（2026-09-27 踩坑，openai 侧同修）────────────────────────────
// 上游长思考静默期被前置 LB 掐断 → 客户端读到 unexpected EOF（无 message_stop）。

// TestAnthropicStreamRetriesEmptyThenSucceeds：首请求空流断开，重试后正常完成。
func TestAnthropicStreamRetriesEmptyThenSucceeds(t *testing.T) {
	shortRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if atomic.AddInt32(&hits, 1) == 1 {
			return // 空流直接断开
		}
		for _, l := range []string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":10}}}`,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"重试成功"}}`,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "%s\n\n", l)
		}
	}))
	t.Cleanup(srv.Close)
	p := NewAnthropicProvider("test-key", srv.URL)

	msg, err := anthropicCollect(t, p)

	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits))
	assert.Equal(t, "重试成功", msg.Text)
	assert.Equal(t, ai.StopReasonStop, msg.StopReason)
}

// TestAnthropicStreamEmptyExhaustsRetries：连续空流，重试耗尽后必须报错。
func TestAnthropicStreamEmptyExhaustsRetries(t *testing.T) {
	shortRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		atomic.AddInt32(&hits, 1)
	}))
	t.Cleanup(srv.Close)
	p := NewAnthropicProvider("test-key", srv.URL)

	msg, err := anthropicCollect(t, p)

	require.Error(t, err)
	assert.Equal(t, int32(streamRetryAttempts), atomic.LoadInt32(&hits))
	assert.Equal(t, ai.StopReasonError, msg.StopReason)
}

func TestAnthropicLiveThinkingAndCachedInputUsage(t *testing.T) {
	srv := anthropicSSEServer(t,
		`event: message_start`, `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":90,"cache_creation_input_tokens":20}}}`,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thought"}}`,
		`event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`event: message_stop`, `data: {"type":"message_stop"}`)
	stream, err := NewAnthropicProvider("test", srv.URL).Stream(context.Background(), ai.StreamRequest{})
	require.NoError(t, err)
	thought := ""
	for event := range stream.Events() {
		if e, ok := event.(ai.EventThinkingDelta); ok {
			thought += e.Delta
		}
	}
	msg, err := stream.Result()
	require.NoError(t, err)
	require.Equal(t, "thought", thought)
	require.Equal(t, 120, msg.Usage.InputTokens)
	require.Equal(t, 90, *msg.Usage.CachedInputTokens)
}

// ─── 回归：模型请求瞬态失败重试（issue #251，openai 侧同修）─────────────────────

// TestAnthropicStreamRetries429ThenSucceeds：429 → 200 时按 Retry-After 退避后重试。
func TestAnthropicStreamRetries429ThenSucceeds(t *testing.T) {
	fastModelRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range []string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":10}}}`,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"重试成功"}}`,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":0}`,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "%s\n\n", l)
		}
	}))
	t.Cleanup(srv.Close)
	p := NewAnthropicProvider("test-key", srv.URL)

	msg, err := anthropicCollect(t, p)

	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits))
	assert.Equal(t, "重试成功", msg.Text)
	assert.Equal(t, ai.StopReasonStop, msg.StopReason)
}

// TestAnthropicStreamHTTP4xxNoRetry：401 鉴权失败不重试，错误消息只含状态码与简短原因。
func TestAnthropicStreamHTTP4xxNoRetry(t *testing.T) {
	fastModelRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	p := NewAnthropicProvider("bad-key", srv.URL)

	msg, err := anthropicCollect(t, p)

	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
	assert.Contains(t, err.Error(), "401")
	assert.NotContains(t, err.Error(), "\n", "错误消息应为单行简短原因")
	assert.Equal(t, ai.StopReasonError, msg.StopReason)
}

// TestAnthropicNoRetryAfterContentStarted：内容已开始输出后断开，不重试以免重复。
func TestAnthropicNoRetryAfterContentStarted(t *testing.T) {
	fastModelRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"已输出\"}}\n\n")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(srv.Close)
	p := NewAnthropicProvider("test-key", srv.URL)

	msg, err := anthropicCollect(t, p)

	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits), "内容已开始，不应重试")
	assert.Equal(t, ai.StopReasonError, msg.StopReason)
}
