package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hwj123hwj/easyagent/sdk/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件是 OpenAI SSE 组装的回归测试。用例来源：
//   - 2026-05-29 踩坑：兼容网关 tool_calls[].index 从 1 开始导致 ToolCalls 丢失
//     （agent-lessons/issues/2026-05-29-easyagent-openai-sse-tool-call-index.md）
//   - litellm-gateway 生产经验：usage 独立 chunk（choices 为空）、超长参数累积、
//     坏行容错、[DONE] 缺失等兼容性差异
//
// 全部走完整 Stream 路径（httptest + HTTP），测的是 provider 层行为而非纯函数。

// ─── 测试基础设施 ────────────────────────────────────────────────────────────────

// sseServer 启动一个依次输出给定 data 帧的 SSE 服务器。帧不经 [DONE] 包装，
// 需要 [DONE] 时显式传入 "[DONE]"。
func sseServer(t *testing.T, frames ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			fmt.Fprintf(w, "data: %s\n\n", f)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// rawSSEServer 输出完全原始的行（含 data: 前缀本身），用于测试非标准帧格式。
func rawSSEServer(t *testing.T, lines ...string) *httptest.Server {
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

// collectStream 走完整 Stream 路径，收集全部事件与最终结果。
func collectStream(t *testing.T, p *OpenAIProvider) ([]ai.Event, ai.StreamAssistantMessage, error) {
	t.Helper()
	stream, err := p.Stream(context.Background(), ai.StreamRequest{
		Model: ai.Model{ID: "test-model"},
	})
	require.NoError(t, err)

	var events []ai.Event
	for ev := range stream.Events() {
		events = append(events, ev)
	}
	msg, err := stream.Result()
	return events, msg, err
}

// ─── SSE 帧构造辅助 ──────────────────────────────────────────────────────────────

func textChunk(content string) string {
	return fmt.Sprintf(`{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, content)
}

func roleChunk() string {
	return `{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`
}

// tcChunk 构造一条 tool_calls 增量帧。args 是原始 JSON 片段，可为空串。
func tcChunk(idx int, id, name, args string) string {
	return fmt.Sprintf(`{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":%d,"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":null}]}`, idx, id, name, args)
}

func finishChunk(reason string) string {
	return fmt.Sprintf(`{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":%q}]}`, reason)
}

// usageChunk 模拟 usage 随独立 chunk 下发（choices 为空）的网关行为。
func usageChunk() string {
	return `{"id":"1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
}

// toolCallEvents 按顺序取出事件流中的全部 ToolCallStart/Delta/End。
func toolCallEvents(events []ai.Event) (starts []ai.EventToolCallStart, deltas []ai.EventToolCallDelta, ends []ai.EventToolCallEnd) {
	for _, ev := range events {
		switch e := ev.(type) {
		case ai.EventToolCallStart:
			starts = append(starts, e)
		case ai.EventToolCallDelta:
			deltas = append(deltas, e)
		case ai.EventToolCallEnd:
			ends = append(ends, e)
		}
	}
	return
}

// ─── 回归：tool_calls index 起始值 ───────────────────────────────────────────────

// TestStreamToolCallIndexFromOne 是 2026-05-29 踩坑的直接回归：
// 兼容网关（mimo-opus 等）index 从 1 开始，旧代码假设 0 起始导致 ToolCalls 丢失、
// Agent 循环提前结束。
func TestStreamToolCallIndexFromOne(t *testing.T) {
	srv := sseServer(t,
		roleChunk(),
		tcChunk(1, "call_a", "read", `{"pa`),
		tcChunk(1, "", "", `th":"main.go"}`),
		finishChunk("tool_calls"),
		"[DONE]",
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	events, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, ai.StopReasonToolUse, msg.StopReason)
	require.Len(t, msg.ToolCalls, 1)
	assert.Equal(t, ai.ToolCall{ID: "call_a", Name: "read", Args: `{"path":"main.go"}`}, msg.ToolCalls[0])

	starts, deltas, ends := toolCallEvents(events)
	require.Len(t, starts, 1)
	require.Len(t, deltas, 1)
	require.Len(t, ends, 1)
	assert.Equal(t, `{"path":"main.go"}`, deltas[0].Delta)
	assert.Equal(t, ai.ToolCall{ID: "call_a", Name: "read", Args: `{"path":"main.go"}`}, ends[0].ToolCall)
}

// TestStreamToolCallIndexFromZero 锁定标准 OpenAI 行为（index 0 起始），与 FromOne 对照。
func TestStreamToolCallIndexFromZero(t *testing.T) {
	srv := sseServer(t,
		tcChunk(0, "call_b", "bash", `{"cmd":"ls"}`),
		finishChunk("tool_calls"),
		"[DONE]",
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, ai.StopReasonToolUse, msg.StopReason)
	require.Len(t, msg.ToolCalls, 1)
	assert.Equal(t, ai.ToolCall{ID: "call_b", Name: "bash", Args: `{"cmd":"ls"}`}, msg.ToolCalls[0])
}

// ─── 多工具调用交错碎片化 ────────────────────────────────────────────────────────

// TestStreamMultipleToolCallsFragmented 模拟并行工具调用：两个 index 的增量
// 交错到达，参数分片拼接。最终必须按 index 排序、参数无损。
func TestStreamMultipleToolCallsFragmented(t *testing.T) {
	srv := sseServer(t,
		tcChunk(0, "c1", "bash", `{"cmd":"`),
		tcChunk(1, "c2", "read", `{"pa`),
		tcChunk(0, "", "", `ls -la`),
		tcChunk(1, "", "", `th":"x.go"}`),
		tcChunk(0, "", "", `"}`),
		finishChunk("tool_calls"),
		"[DONE]",
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	require.Len(t, msg.ToolCalls, 2)
	assert.Equal(t, ai.ToolCall{ID: "c1", Name: "bash", Args: `{"cmd":"ls -la"}`}, msg.ToolCalls[0])
	assert.Equal(t, ai.ToolCall{ID: "c2", Name: "read", Args: `{"path":"x.go"}`}, msg.ToolCalls[1])
}

// ─── 文本 + 工具调用混合 ────────────────────────────────────────────────────────

// TestStreamTextThenToolCalls 验证文本与工具调用共存，且事件协议完整有序：
// Start → TextStart → TextDelta → TextEnd → ToolCall* → Done。
// ContentIndex 必须连续（text=0，tool 从 1 起），不透传上游 index——
// 1-based 网关会在这里留下空洞（网关侧 ForwardStream 用自增序号的教训）。
func TestStreamTextThenToolCalls(t *testing.T) {
	srv := sseServer(t,
		textChunk("我来看一下这个文件"),
		tcChunk(1, "call_a", "read", `{"path":"a.go"}`),
		finishChunk("tool_calls"),
		"[DONE]",
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	events, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, ai.StopReasonToolUse, msg.StopReason)
	assert.Equal(t, "我来看一下这个文件", msg.Text)
	require.Len(t, msg.ToolCalls, 1)

	// 事件序列与 ContentIndex 连续性
	var kinds []string
	for _, ev := range events {
		switch ev.(type) {
		case ai.EventStart:
			kinds = append(kinds, "start")
		case ai.EventTextStart:
			kinds = append(kinds, "textStart")
		case ai.EventTextDelta:
			kinds = append(kinds, "textDelta")
		case ai.EventTextEnd:
			kinds = append(kinds, "textEnd")
		case ai.EventToolCallStart:
			kinds = append(kinds, "toolStart")
		case ai.EventToolCallDelta:
			kinds = append(kinds, "toolDelta")
		case ai.EventToolCallEnd:
			kinds = append(kinds, "toolEnd")
		case ai.EventDone:
			kinds = append(kinds, "done")
		}
	}
	assert.Equal(t, []string{"start", "textStart", "textDelta", "textEnd", "toolStart", "toolDelta", "toolEnd", "done"}, kinds)

	starts, _, _ := toolCallEvents(events)
	require.Len(t, starts, 1)
	assert.Equal(t, 1, starts[0].ContentIndex, "工具事件 ContentIndex 应紧跟文本 block，不透传上游 index")
}

// ─── 网关兼容性差异（litellm-gateway 生产经验） ─────────────────────────────────

// TestStreamSkipsEmptyChoicesAndUsageChunk：usage 随 choices 为空的独立 chunk 下发
// 是常见网关行为（OpenAI stream_options.include_usage 等），必须跳过而非中断。
func TestStreamSkipsEmptyChoicesAndUsageChunk(t *testing.T) {
	srv := sseServer(t,
		roleChunk(),
		usageChunk(),
		textChunk("hi"),
		finishChunk("stop"),
		"[DONE]",
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	events, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, ai.StopReasonStop, msg.StopReason)
	assert.Equal(t, "hi", msg.Text)
	for _, ev := range events {
		_, isError := ev.(ai.EventError)
		assert.False(t, isError, "空 choices/usage chunk 不应触发错误事件")
	}
}

// TestStreamMalformedLineIgnored：流中夹杂坏 JSON 行时跳过继续，不中断组装。
func TestStreamMalformedLineIgnored(t *testing.T) {
	srv := sseServer(t,
		textChunk("hello "),
		`{not valid json`,
		textChunk("world"),
		finishChunk("stop"),
		"[DONE]",
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, "hello world", msg.Text)
}

// TestStreamWithoutDoneMarker：部分网关不发 data: [DONE]，流直接结束。
// 组装必须正常收尾。
func TestStreamWithoutDoneMarker(t *testing.T) {
	srv := sseServer(t,
		textChunk("bye"),
		finishChunk("stop"),
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, ai.StopReasonStop, msg.StopReason)
	assert.Equal(t, "bye", msg.Text)
}

// TestStreamLargeArgumentsAccumulate：write 类工具的参数可达数十 KB 且分片
// 到达（网关因此把 scanner buffer 加到 256KB），验证大参数累积无损。
func TestStreamLargeArgumentsAccumulate(t *testing.T) {
	piece := strings.Repeat("x", 8192)
	frames := []string{tcChunk(0, "c1", "write", `{"path":"a.txt","content":"`)}
	for i := 0; i < 8; i++ {
		frames = append(frames, tcChunk(0, "", "", piece))
	}
	frames = append(frames, tcChunk(0, "", "", `"}`), finishChunk("tool_calls"), "[DONE]")

	srv := sseServer(t, frames...)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	require.Len(t, msg.ToolCalls, 1)
	want := `{"path":"a.txt","content":"` + strings.Repeat(piece, 8) + `"}`
	assert.Equal(t, want, msg.ToolCalls[0].Args)
}

// ─── new-api 交叉验证补充的兼容用例 ─────────────────────────────────────────────

// TestStreamNoSpaceDataPrefix：部分网关发 data: 不带空格（new-api stream_scanner
// 为此用 data[5:]+TrimSpace 解析）。旧实现只认 "data: "，会静默丢弃整条消息。
// 同时覆盖 data:[DONE]（无空格）终止符。
func TestStreamNoSpaceDataPrefix(t *testing.T) {
	srv := rawSSEServer(t,
		`data:{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`data:{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":" there"},"finish_reason":null}]}`,
		`data:{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data:[DONE]`,
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, "hi there", msg.Text)
	assert.Equal(t, ai.StopReasonToolUse, msg.StopReason)
}

// TestStreamHugeSingleLineArguments：单行超过旧 256KB scanner 上限会被截断
// （new-api 因此把上限放到 128MB）。验证 300KB 单行参数无损。
func TestStreamHugeSingleLineArguments(t *testing.T) {
	big := strings.Repeat("y", 300*1024)
	srv := sseServer(t,
		tcChunk(0, "c1", "write", `{"path":"b.txt","content":"`),
		tcChunk(0, "", "", big),
		tcChunk(0, "", "", `"}`),
		finishChunk("tool_calls"),
		"[DONE]",
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	require.Len(t, msg.ToolCalls, 1)
	want := `{"path":"b.txt","content":"` + big + `"}`
	assert.Equal(t, want, msg.ToolCalls[0].Args)
}

// TestStreamToolCallMissingIndex：部分上游的 tool_calls 增量不带 index 字段，
// 零值并入 key 0，单工具场景必须正常组装（new-api 用 *int 区分缺失，此处
// 锁定 easyagent 的零值兜底行为）。
func TestStreamToolCallMissingIndex(t *testing.T) {
	frame := `{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_x","type":"function","function":{"name":"grep","arguments":"{\"q\":\"foo\"}"}}]},"finish_reason":null}]}`
	srv := sseServer(t,
		frame,
		finishChunk("tool_calls"),
		"[DONE]",
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	require.Len(t, msg.ToolCalls, 1)
	assert.Equal(t, ai.ToolCall{ID: "call_x", Name: "grep", Args: `{"q":"foo"}`}, msg.ToolCalls[0])
}

// ─── 推理内容（reasoning_content） ──────────────────────────────────────────────

func reasoningChunk(content string) string {
	return fmt.Sprintf(`{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"reasoning_content":%q},"finish_reason":null}]}`, content)
}

// TestStreamReasoningContent：deepseek 系推理内容累积进 partial.Thinking，
// 与文本/工具调用共存，思考同时流式发送并保留在最终消息。
func TestStreamReasoningContent(t *testing.T) {
	srv := sseServer(t,
		roleChunk(),
		reasoningChunk("用户想要读取文件，"),
		reasoningChunk("我应该先调用 read 工具。"),
		textChunk("我来读取这个文件。"),
		tcChunk(0, "c1", "read", `{"path":"a.go"}`),
		finishChunk("tool_calls"),
		"[DONE]",
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, "用户想要读取文件，我应该先调用 read 工具。", msg.Thinking)
	assert.Equal(t, "我来读取这个文件。", msg.Text)
	require.Len(t, msg.ToolCalls, 1)
	assert.Equal(t, "read", msg.ToolCalls[0].Name)
}

// TestStreamOpenRouterStyleReasoning：OpenRouter 风格的 reasoning 字段同样累积。
func TestStreamOpenRouterStyleReasoning(t *testing.T) {
	frame := `{"id":"1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"reasoning":"hmm"},"finish_reason":null}]}`
	srv := sseServer(t,
		frame,
		finishChunk("stop"),
		"[DONE]",
	)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, "hmm", msg.Thinking)
	assert.Equal(t, "", msg.Text)
}

// ─── 空闲超时 ───────────────────────────────────────────────────────────────────

// TestStreamIdleTimeout：上游停滞（首块后不再发数据）超过空闲阈值时，
// 流以错误收尾而非无限等待或伪装成功；停滞前已收到的内容保留。
func TestStreamIdleTimeout(t *testing.T) {
	old := streamIdleTimeout
	streamIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+textChunk("par")+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // 停滞：直到客户端断开都不再发数据
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	events, msg, err := collectStream(t, p)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "idle")
	assert.Equal(t, ai.StopReasonError, msg.StopReason)
	assert.Equal(t, "par", msg.Text, "停滞前收到的内容应保留")
	foundErr := false
	for _, ev := range events {
		if _, ok := ev.(ai.EventError); ok {
			foundErr = true
		}
	}
	assert.True(t, foundErr, "应以 EventError 收尾")
}

// ─── finish_reason 映射 ─────────────────────────────────────────────────────────

func TestStreamFinishReasonMapping(t *testing.T) {
	tests := []struct {
		name   string
		finish string // 空 = 不发 finish_reason
		want   ai.StopReason
	}{
		{"stop", "stop", ai.StopReasonStop},
		{"tool_calls", "tool_calls", ai.StopReasonToolUse},
		{"length", "length", ai.StopReasonLength},
		{"missing_defaults_to_stop", "", ai.StopReasonStop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames := []string{textChunk("ok")}
			if tt.finish != "" {
				frames = append(frames, finishChunk(tt.finish))
			}
			frames = append(frames, "[DONE]")

			srv := sseServer(t, frames...)
			p := NewOpenAIProvider("test-key", srv.URL)

			_, msg, err := collectStream(t, p)

			require.NoError(t, err)
			assert.Equal(t, tt.want, msg.StopReason)
		})
	}
}

// ─── 传输层错误 ─────────────────────────────────────────────────────────────────

// TestStreamHTTPError：非 200 响应必须产生 EventError 且 Result 带错误，
// Agent 循环据此走重试/终止路径。
func TestStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("bad-key", srv.URL)

	events, msg, err := collectStream(t, p)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Equal(t, ai.StopReasonError, msg.StopReason)
	found := false
	for _, ev := range events {
		if _, ok := ev.(ai.EventError); ok {
			found = true
		}
	}
	assert.True(t, found, "应产生 EventError 事件")
}

// ─── 回归：断流重试（2026-09-27 踩坑）───────────────────────────────────────────
// 场景：glm coding 端点长思考静默期被前置 LB 掐断，客户端读到 unexpected EOF，
// 或收到无内容的 finish_reason（网关补的假帧）。TUI 直接显示 "Error: unexpected EOF"。

// shortRetry 把退避间隔缩到毫秒级，测试不真等 1s/2s。
func shortRetry(t *testing.T) {
	t.Helper()
	old := streamRetryDelay
	streamRetryDelay = 5 * time.Millisecond
	t.Cleanup(func() { streamRetryDelay = old })
}

// TestStreamRetriesEmptyThenSucceeds：第一次请求空流断开（EOF），重试后拿到正常内容。
func TestStreamRetriesEmptyThenSucceeds(t *testing.T) {
	shortRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if atomic.AddInt32(&hits, 1) == 1 {
			// 首次：不发任何 data 直接断开 → scanner.Err() = unexpected EOF
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", textChunk("恢复后的回答"))
		fmt.Fprintf(w, "data: %s\n\n", finishChunk("stop"))
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	events, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits), "should retry exactly once")
	assert.Equal(t, "恢复后的回答", msg.Text)
	assert.Equal(t, ai.StopReasonStop, msg.StopReason)

	// 只应有一次 EventStart（重试不重复推流开始事件）
	starts := 0
	for _, ev := range events {
		if _, ok := ev.(ai.EventStart); ok {
			starts++
		}
	}
	assert.Equal(t, 1, starts)
}

// TestStreamRetriesFakeFinishReason：上游断流后网关补了无内容的 finish_reason=length（假帧），
// 同样要重试而不是把空消息标成 length 收场。
func TestStreamRetriesFakeFinishReason(t *testing.T) {
	shortRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if atomic.AddInt32(&hits, 1) == 1 {
			fmt.Fprintf(w, "data: %s\n\n", finishChunk("length"))
			fmt.Fprintf(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", textChunk("第二次成功"))
		fmt.Fprintf(w, "data: %s\n\n", finishChunk("stop"))
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits))
	assert.Equal(t, "第二次成功", msg.Text)
	assert.Equal(t, ai.StopReasonStop, msg.StopReason)
}

// TestStreamEmptyExhaustsRetries：连续空流，重试耗尽后必须报错，不能静默空收场。
func TestStreamEmptyExhaustsRetries(t *testing.T) {
	shortRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		atomic.AddInt32(&hits, 1)
		// 空流直接断开
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.Error(t, err)
	assert.Equal(t, int32(streamRetryAttempts), atomic.LoadInt32(&hits))
	assert.Equal(t, ai.StopReasonError, msg.StopReason)
	assert.Contains(t, err.Error(), "empty stream")
}

func TestStreamUsageOnlyFrameAndLiveThinking(t *testing.T) {
	srv := sseServer(t, reasoningChunk("first"), textChunk("answer"), finishChunk("stop"), `{"choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":25,"prompt_tokens_details":{"cached_tokens":800}}}`, "[DONE]")
	events, msg, err := collectStream(t, NewOpenAIProvider("test", srv.URL))
	require.NoError(t, err)
	require.Equal(t, 1200, msg.Usage.InputTokens)
	require.Equal(t, 25, msg.Usage.OutputTokens)
	require.NotNil(t, msg.Usage.CachedInputTokens)
	require.Equal(t, 800, *msg.Usage.CachedInputTokens)
	thinkingIndex, textIndex := -1, -1
	for i, event := range events {
		if e, ok := event.(ai.EventThinkingDelta); ok {
			thinkingIndex = i
			require.Equal(t, "first", e.Delta)
		}
		if _, ok := event.(ai.EventTextDelta); ok {
			textIndex = i
		}
	}
	require.GreaterOrEqual(t, thinkingIndex, 0)
	require.Greater(t, textIndex, thinkingIndex)
}

// ─── 回归：模型请求瞬态失败重试（issue #251）────────────────────────────────────
// 429/5xx 与传输错误在收到 200 响应头之前重试；开始输出后不重试，避免界面内容重复。

// fastModelRetry 把瞬态失败的退避缩到毫秒级，测试不真等 500ms/1s。
func fastModelRetry(t *testing.T) {
	t.Helper()
	oldBase, oldMax := modelRetryBackoffBase, modelRetryBackoffMax
	modelRetryBackoffBase = 2 * time.Millisecond
	modelRetryBackoffMax = 5 * time.Millisecond
	t.Cleanup(func() { modelRetryBackoffBase, modelRetryBackoffMax = oldBase, oldMax })
}

// TestStreamRetries429Then503ThenSucceeds：429 → 503 → 200 依次返回时该轮正常完成。
func TestStreamRetries429Then503ThenSucceeds(t *testing.T) {
	fastModelRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&hits, 1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			http.Error(w, `{"error":{"message":"rate limited"}}`, http.StatusTooManyRequests)
		case 2:
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", textChunk("重试成功"))
			fmt.Fprintf(w, "data: %s\n\n", finishChunk("stop"))
			fmt.Fprintf(w, "data: [DONE]\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	events, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, int32(3), atomic.LoadInt32(&hits))
	assert.Equal(t, "重试成功", msg.Text)
	assert.Equal(t, ai.StopReasonStop, msg.StopReason)

	// EventStart 只推一次，重试不产生重复内容
	starts := 0
	for _, ev := range events {
		if _, ok := ev.(ai.EventStart); ok {
			starts++
		}
	}
	assert.Equal(t, 1, starts)
}

// TestStreamHTTP4xxNoRetry：400 属请求错误，不重试。
func TestStreamHTTP4xxNoRetry(t *testing.T) {
	fastModelRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, `{"error":{"message":"invalid request"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
	assert.Contains(t, err.Error(), "400")
	assert.Equal(t, ai.StopReasonError, msg.StopReason)
}

// TestStreamHTTPRetryExhaustsBudget：默认上限 2 次，耗尽后以最后一个状态码报错。
func TestStreamHTTPRetryExhaustsBudget(t *testing.T) {
	fastModelRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.Error(t, err)
	assert.Equal(t, int32(3), atomic.LoadInt32(&hits), "首次请求 + 2 次重试")
	assert.Contains(t, err.Error(), "503")
	assert.Equal(t, ai.StopReasonError, msg.StopReason)
}

// TestStreamModelRetriesEnvDisable：EA_MODEL_MAX_RETRIES=0 时完全关闭瞬态重试。
func TestStreamModelRetriesEnvDisable(t *testing.T) {
	fastModelRetry(t)
	t.Setenv("EA_MODEL_MAX_RETRIES", "0")

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, _, err := collectStream(t, p)

	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
}

// TestStreamModelRetriesEnvInvalid：非法配置在 Stream 返回前同步报错。
func TestStreamModelRetriesEnvInvalid(t *testing.T) {
	t.Setenv("EA_MODEL_MAX_RETRIES", "abc")

	p := NewOpenAIProvider("test-key", "http://127.0.0.1:1")
	_, err := p.Stream(context.Background(), ai.StreamRequest{Model: ai.Model{ID: "test-model"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EA_MODEL_MAX_RETRIES")
}

// TestStreamRetryStopsOnContextCancel：退避等待期间 ctx 取消，立即终止不再等待。
func TestStreamRetryStopsOnContextCancel(t *testing.T) {
	fastModelRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Retry-After", "10")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	stream, err := p.Stream(ctx, ai.StreamRequest{Model: ai.Model{ID: "test-model"}})
	require.NoError(t, err)
	for range stream.Events() {
	}
	_, resultErr := stream.Result()

	require.Error(t, resultErr)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits))
	assert.Contains(t, resultErr.Error(), "context canceled")
}

// TestStreamNoRetryAfterContentStarted：内容已开始输出后断开，不重试以免重复。
func TestStreamNoRetryAfterContentStarted(t *testing.T) {
	fastModelRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", textChunk("已输出一半"))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // 强制断开连接
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&hits), "内容已开始，不应重试")
	assert.Equal(t, ai.StopReasonError, msg.StopReason)
}

// TestStreamRetriesTransportError：响应头之前连接被掐断，按瞬态失败重试。
func TestStreamRetriesTransportError(t *testing.T) {
	fastModelRetry(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			panic(http.ErrAbortHandler) // 未写响应头直接断开
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", textChunk("传输重试成功"))
		fmt.Fprintf(w, "data: %s\n\n", finishChunk("stop"))
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	_, msg, err := collectStream(t, p)

	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits))
	assert.Equal(t, "传输重试成功", msg.Text)
}

// TestStreamRetryHonorsRetryAfter：上游给了 Retry-After 时按其等待（不缩短退避）。
func TestStreamRetryHonorsRetryAfter(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", textChunk("ok"))
		fmt.Fprintf(w, "data: %s\n\n", finishChunk("stop"))
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAIProvider("test-key", srv.URL)

	started := time.Now()
	_, msg, err := collectStream(t, p)
	elapsed := time.Since(started)

	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits))
	assert.GreaterOrEqual(t, elapsed, 900*time.Millisecond, "应等待 Retry-After 指定的 1s")
	assert.Equal(t, "ok", msg.Text)
}

func TestStreamLongRetryAfterDoesNotRetryEarly(t *testing.T) {
	fastModelRetry(t)
	old := modelRetryAfterMax
	modelRetryAfterMax = 10 * time.Millisecond
	t.Cleanup(func() { modelRetryAfterMax = old })
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "120")
		http.Error(w, "busy", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	_, _, err := collectStream(t, NewOpenAIProvider("test-key", srv.URL))
	require.Error(t, err)
	assert.Equal(t, int32(1), hits.Load())
	var status *ai.HTTPStatusError
	require.ErrorAs(t, err, &status)
	assert.Equal(t, 2*time.Minute, status.RetryAfter)
}

func TestStreamHTTPAndEmptyRetryBudgetsAreIndependent(t *testing.T) {
	fastModelRetry(t)
	old := streamRetryDelay
	streamRetryDelay = time.Millisecond
	t.Cleanup(func() { streamRetryDelay = old })
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch hits.Add(1) {
		case 1:
			http.Error(w, "busy", 429)
		case 2:
			http.Error(w, "busy", 503)
		case 3:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finishChunk("stop"))
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", textChunk("recovered"), finishChunk("stop"))
		}
	}))
	t.Cleanup(srv.Close)
	_, msg, err := collectStream(t, NewOpenAIProvider("test-key", srv.URL))
	require.NoError(t, err)
	assert.Equal(t, int32(4), hits.Load())
	assert.Equal(t, "recovered", msg.Text)
}
