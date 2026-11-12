package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/stretchr/testify/require"
)

// ─── Codex Responses API SSE 回归测试 ────────────────────────────────────────────
// 与 openai_test.go 同一套路：httptest + 完整 Stream 路径。
// SSE 事件类型参照 OpenAI Responses API（chatgpt.com/backend-api/codex/responses）。

// codexEvent 是一条 SSE 事件（event: + data:）。
type codexEvent struct {
	Event string
	Data  string
}

type codexTokenSource struct {
	token string
	count int
}

func (s *codexTokenSource) AccessToken(context.Context) (string, error) { return s.token, nil }
func (s *codexTokenSource) RefreshAfterRejection(context.Context, string) (string, error) {
	s.count++
	return s.token, nil
}

// codexEvents 启动一个依次输出给定 SSE 事件的测试服务器，可选捕获请求体与头。
func codexEvents(t *testing.T, evs []codexEvent, capture *[]byte, headers *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if capture != nil {
			*capture = body
		}
		if headers != nil {
			*headers = r.Header.Clone()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range evs {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, ev.Data)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newCodexProvider(t *testing.T, srv *httptest.Server, tokens *codexTokenSource) *CodexProvider {
	t.Helper()
	if tokens == nil {
		tokens = &codexTokenSource{token: "acc-test"}
	}
	p := NewCodexProvider("account-1", tokens)
	p.baseURL = srv.URL + "/codex"
	return p
}

func collectCodexStream(t *testing.T, p *CodexProvider, req ai.StreamRequest) ([]ai.Event, ai.StreamAssistantMessage, error) {
	t.Helper()
	stream, err := p.Stream(context.Background(), req)
	if err != nil {
		return nil, ai.StreamAssistantMessage{}, err
	}
	var events []ai.Event
	for ev := range stream.Events() {
		events = append(events, ev)
	}
	msg, err := stream.Result()
	return events, msg, err
}

func textStartEvent(id string) codexEvent {
	return codexEvent{Event: "response.output_item.added", Data: fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant","id":%q,"status":"in_progress","content":[]}}`, id)}
}

func textDeltaEvent(delta string) codexEvent {
	return codexEvent{Event: "response.output_text.delta", Data: fmt.Sprintf(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":%q}`, delta)}
}

func textDoneEvent(text string) codexEvent {
	return codexEvent{Event: "response.output_text.done", Data: fmt.Sprintf(`{"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":%q}`, text)}
}

func messageDoneEvent(text string) codexEvent {
	return codexEvent{Event: "response.output_item.done", Data: fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","id":"msg_1","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}}`, text)}
}

func responseCompletedEvent() codexEvent {
	return codexEvent{Event: "response.completed", Data: `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":2}}}}`}
}

// ─── 纯文本流 ────────────────────────────────────────────────────────────────────

func TestCodexStreamText(t *testing.T) {
	var body []byte
	var headers http.Header
	srv := codexEvents(t, []codexEvent{
		{"response.created", `{"type":"response.created","response":{"id":"resp_1"}}`},
		textStartEvent("msg_1"),
		textDeltaEvent("你好"),
		textDeltaEvent("，世界"),
		textDoneEvent("你好，世界"),
		messageDoneEvent("你好，世界"),
		responseCompletedEvent(),
	}, &body, &headers)

	p := newCodexProvider(t, srv, nil)
	events, msg, err := collectCodexStream(t, p, ai.StreamRequest{
		Model:  ai.Model{ID: "gpt-5.2-codex"},
		System: "you are helpful",
	})
	require.NoError(t, err)
	require.NoError(t, err)

	// 请求头必须带 Codex 身份
	require.Equal(t, "Bearer acc-test", headers.Get("Authorization"))
	require.Equal(t, "account-1", headers.Get("chatgpt-account-id"))
	require.Equal(t, "application/json", headers.Get("Content-Type"))

	// 请求体走 Responses API
	bodyStr := string(body)
	require.Contains(t, bodyStr, `"model":"gpt-5.2-codex"`)
	require.Contains(t, bodyStr, `"stream":true`)
	require.Contains(t, bodyStr, `"instructions":"you are helpful"`)
	require.Contains(t, bodyStr, `"store":false`)

	var text strings.Builder
	for _, ev := range events {
		if e, ok := ev.(ai.EventTextDelta); ok {
			text.WriteString(e.Delta)
		}
	}
	require.Equal(t, "你好，世界", text.String())
	require.Equal(t, ai.StopReasonStop, msg.StopReason)
	require.Equal(t, 10, msg.Usage.InputTokens)
	require.Equal(t, 5, msg.Usage.OutputTokens)
	require.NotNil(t, msg.Usage.CachedInputTokens)
	require.Equal(t, 4, *msg.Usage.CachedInputTokens)
}

// ─── 工具调用流 ──────────────────────────────────────────────────────────────────

func toolCallStartEvent() codexEvent {
	return codexEvent{Event: "response.output_item.added", Data: `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"","status":"in_progress"}}`}
}

func toolCallArgsDeltaEvent(delta string) codexEvent {
	return codexEvent{Event: "response.function_call_arguments.delta", Data: fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":%q}`, delta)}
}

func toolCallDoneEvent(args string) codexEvent {
	return codexEvent{Event: "response.output_item.done", Data: fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":%q,"status":"completed"}}`, args)}
}

func TestCodexStreamToolCall(t *testing.T) {
	srv := codexEvents(t, []codexEvent{
		{"response.created", `{"type":"response.created","response":{"id":"resp_1"}}`},
		toolCallStartEvent(),
		toolCallArgsDeltaEvent(`{"pa`),
		toolCallArgsDeltaEvent(`th":"main.go"}`),
		toolCallDoneEvent(`{"path":"main.go"}`),
		responseCompletedEvent(),
	}, nil, nil)

	p := newCodexProvider(t, srv, nil)
	events, msg, err := collectCodexStream(t, p, ai.StreamRequest{Model: ai.Model{ID: "gpt-5.2-codex"}})
	require.NoError(t, err)

	require.Equal(t, ai.StopReasonToolUse, msg.StopReason)
	require.Len(t, msg.ToolCalls, 1)
	require.Equal(t, "call_1", msg.ToolCalls[0].ID)
	require.Equal(t, "read_file", msg.ToolCalls[0].Name)
	require.JSONEq(t, `{"path":"main.go"}`, msg.ToolCalls[0].Args)

	starts, deltas, ends := 0, "", 0
	for _, ev := range events {
		switch e := ev.(type) {
		case ai.EventToolCallStart:
			starts++
		case ai.EventToolCallDelta:
			deltas += e.Delta
		case ai.EventToolCallEnd:
			ends++
		}
	}
	require.Equal(t, 1, starts)
	require.Equal(t, `{"path":"main.go"}`, deltas)
	require.Equal(t, 1, ends)
}

// ─── 多轮：assistant 回放 + tool result 转换 ─────────────────────────────────────

func TestCodexRequestConvertsHistory(t *testing.T) {
	var body []byte
	srv := codexEvents(t, []codexEvent{
		textStartEvent("msg_1"),
		textDeltaEvent("ok"),
		messageDoneEvent("ok"),
		responseCompletedEvent(),
	}, &body, nil)
	p := newCodexProvider(t, srv, nil)

	req := ai.StreamRequest{
		Model:  ai.Model{ID: "gpt-5.2-codex"},
		System: "sys prompt",
		Messages: []ai.Message{
			ai.NewTextUserMessage("列出文件"),
			ai.AssistantMessage{
				Text:      "好的",
				ToolCalls: []ai.ToolCall{{ID: "call_1", Name: "read_file", Args: `{"path":"a.go"}`}},
			},
			ai.ToolResultMessage{ToolCallID: "call_1", Content: "file content"},
		},
		Tools: []ai.ToolDefinition{{Name: "read_file", Description: "read", Parameters: map[string]any{"type": "object"}}},
	}
	_, _, err := collectCodexStream(t, p, req)
	require.NoError(t, err)

	var wire struct {
		Model        string           `json:"model"`
		Instructions string           `json:"instructions"`
		Input        []map[string]any `json:"input"`
		Tools        []struct {
			Type       string `json:"type"`
			Name       string `json:"name"`
			Parameters any    `json:"parameters"`
		} `json:"tools"`
	}
	require.NoError(t, unmarshalStrict(body, &wire))

	require.Equal(t, "sys prompt", wire.Instructions)
	require.Len(t, wire.Tools, 1)
	require.Equal(t, "function", wire.Tools[0].Type)
	require.Equal(t, "read_file", wire.Tools[0].Name)

	// input: [user, message, function_call, function_call_output]
	require.Len(t, wire.Input, 4)
	require.Equal(t, "user", wire.Input[0]["role"])
	require.Equal(t, "message", wire.Input[1]["type"])
	require.Equal(t, "function_call", wire.Input[2]["type"])
	require.Equal(t, "call_1", wire.Input[2]["call_id"])
	require.Equal(t, "read_file", wire.Input[2]["name"])
	require.Equal(t, `{"path":"a.go"}`, wire.Input[2]["arguments"])
	require.Equal(t, "function_call_output", wire.Input[3]["type"])
	require.Equal(t, "call_1", wire.Input[3]["call_id"])
	require.Equal(t, "file content", wire.Input[3]["output"])
}

// ─── 错误事件与 HTTP 错误 ─────────────────────────────────────────────────────────

func TestCodexStreamErrorEvent(t *testing.T) {
	srv := codexEvents(t, []codexEvent{
		{"response.created", `{"type":"response.created","response":{"id":"resp_1"}}`},
		{"error", `{"type":"error","code":"server_error","message":"boom"}`},
	}, nil, nil)
	p := newCodexProvider(t, srv, nil)
	_, _, err := collectCodexStream(t, p, ai.StreamRequest{Model: ai.Model{ID: "gpt-5.2-codex"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
}

func TestCodexStreamIncomplete(t *testing.T) {
	srv := codexEvents(t, []codexEvent{
		{"response.created", `{"type":"response.created","response":{"id":"resp_1"}}`},
		textStartEvent("msg_1"),
		textDeltaEvent("部分输出"),
		{"response.incomplete", `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":1,"output_tokens":1}}}`},
	}, nil, nil)
	p := newCodexProvider(t, srv, nil)
	_, msg, err := collectCodexStream(t, p, ai.StreamRequest{Model: ai.Model{ID: "gpt-5.2-codex"}})
	require.NoError(t, err)
	require.Equal(t, ai.StopReasonLength, msg.StopReason)
}

// ─── 401 触发 token 刷新重试 ─────────────────────────────────────────────────────

type codexRotatingTokens struct {
	current string
	refresh int
}

func (s *codexRotatingTokens) AccessToken(context.Context) (string, error) { return s.current, nil }
func (s *codexRotatingTokens) RefreshAfterRejection(_ context.Context, rejected string) (string, error) {
	s.refresh++
	s.current = "fresh-token"
	return s.current, nil
}

func TestCodexStreamRefreshesOnUnauthorized(t *testing.T) {
	tokens := &codexRotatingTokens{current: "stale-token"}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "Bearer stale-token" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"expired"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []codexEvent{
			textStartEvent("msg_1"),
			textDeltaEvent("ok"),
			messageDoneEvent("ok"),
			responseCompletedEvent(),
		} {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, ev.Data)
		}
	}))
	t.Cleanup(srv.Close)

	p := newCodexProvider(t, srv, nil)
	p.tokens = tokens
	_, msg, err := collectCodexStream(t, p, ai.StreamRequest{Model: ai.Model{ID: "gpt-5.2-codex"}})
	require.NoError(t, err)
	require.Equal(t, 1, tokens.refresh)
	require.Equal(t, 2, calls)
	require.Equal(t, ai.StopReasonStop, msg.StopReason)
}

// 服务端不换发新 token 时不得无限打转。
func TestCodexStreamSameTokenAfterRefreshFails(t *testing.T) {
	tokens := &codexRotatingTokens{current: "same-token"}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"expired"}}`)
	}))
	t.Cleanup(srv.Close)

	p := newCodexProvider(t, srv, nil)
	p.tokens = tokens
	_, _, err := collectCodexStream(t, p, ai.StreamRequest{Model: ai.Model{ID: "gpt-5.2-codex"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "重新登录")
	require.LessOrEqual(t, calls, 2)
}

// ─── 推理摘要流 ──────────────────────────────────────────────────────────────────

func TestCodexStreamReasoningSummary(t *testing.T) {
	srv := codexEvents(t, []codexEvent{
		{"response.created", `{"type":"response.created","response":{"id":"resp_1"}}`},
		{"response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`},
		{"response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"delta":"思考中"}`},
		{"response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"思考中"}]}}`},
		textStartEvent("msg_1"),
		textDeltaEvent("答案"),
		messageDoneEvent("答案"),
		responseCompletedEvent(),
	}, nil, nil)
	p := newCodexProvider(t, srv, nil)
	events, msg, err := collectCodexStream(t, p, ai.StreamRequest{Model: ai.Model{ID: "gpt-5.2-codex"}})
	require.NoError(t, err)

	var thinking strings.Builder
	for _, ev := range events {
		if e, ok := ev.(ai.EventThinkingDelta); ok {
			thinking.WriteString(e.Delta)
		}
	}
	require.Equal(t, "思考中", thinking.String())
	require.Equal(t, "思考中", msg.Thinking)
	require.Equal(t, "答案", msg.Text)
}

// ─── reasoning 回放：assistant 消息带 thinking blocks ────────────────────────────

func TestCodexRequestReplaysReasoning(t *testing.T) {
	var body []byte
	srv := codexEvents(t, []codexEvent{
		textStartEvent("msg_1"),
		textDeltaEvent("ok"),
		messageDoneEvent("ok"),
		responseCompletedEvent(),
	}, &body, nil)
	p := newCodexProvider(t, srv, nil)

	enc := mockEncryptedReasoning("rs_1", "enc-content")
	req := ai.StreamRequest{
		Model: ai.Model{ID: "gpt-5.2-codex"},
		Messages: []ai.Message{
			ai.NewTextUserMessage("hi"),
			ai.AssistantMessage{
				Thinking:       "之前想过",
				ThinkingBlocks: []ai.ThinkingBlock{{Type: "thinking", Thinking: "之前想过", Signature: enc}},
				Text:           "answer",
			},
			ai.NewTextUserMessage("continue"),
		},
	}
	_, _, err := collectCodexStream(t, p, req)
	require.NoError(t, err)

	var wire struct {
		Input []map[string]any `json:"input"`
	}
	require.NoError(t, unmarshalStrict(body, &wire))
	require.Len(t, wire.Input, 4)
	require.Equal(t, "reasoning", wire.Input[1]["type"])
	require.Equal(t, "rs_1", wire.Input[1]["id"])
	require.Equal(t, "message", wire.Input[2]["type"])
}

// unmarshalStrict 便于把请求体解码到匿名结构。
func unmarshalStrict(data []byte, out any) error {
	return jsonUnmarshal(data, out)
}
