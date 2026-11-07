package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
)

type deepVTestTokens struct {
	token   string
	refresh atomic.Int32
}

func (t *deepVTestTokens) AccessToken(context.Context) (string, error) { return t.token, nil }
func (t *deepVTestTokens) RefreshAfterRejection(_ context.Context, previous string) (string, error) {
	if previous != t.token {
		panic("wrong rejected token")
	}
	t.refresh.Add(1)
	return "renewed-token", nil
}

func collectDeepV(t *testing.T, p *DeepVProvider, req ai.StreamRequest) (ai.StreamAssistantMessage, error) {
	t.Helper()
	stream, err := p.Stream(context.Background(), req)
	if err != nil {
		return ai.StreamAssistantMessage{}, err
	}
	for range stream.Events() {
	}
	return stream.Result()
}

func TestDeepVStreamingToolCallAndConversationHistory(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/stream" || r.Header.Get("Authorization") != "Bearer account-token" || r.UserAgent() != "EasyAgent" {
			t.Errorf("invalid request metadata: %s, %s", r.URL.Path, r.UserAgent())
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "deepseek-flash" {
			t.Errorf("wrong model: %v", body["model"])
		}
		if calls.Add(1) == 1 {
			config := body["config"].(map[string]any)
			if config["maxOutputTokens"].(float64) > 32000 {
				t.Error("exceeded upstream token limit")
			}
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"reasoning\":\"think\"},{\"text\":\"Checking\"}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"id\":\"call-1\",\"name\":\"read\",\"args\":\"{\\\"path\\\":\"}},{\"functionCall\":{\"id\":\"call-1\",\"args\":\"\\\"file.txt\\\"}\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"TOOL_CALL\"}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":4,\"cachedContentTokenCount\":3}}\n\ndata: [DONE]\n\n")
		} else {
			contents := body["contents"].([]any)
			last := contents[len(contents)-1].(map[string]any)
			parts := last["parts"].([]any)
			if last["role"] != "user" || len(parts) < 1 {
				t.Errorf("bad tool result turn: %#v", last)
			}
			fr := parts[0].(map[string]any)["functionResponse"].(map[string]any)
			if fr["id"] != "call-1" || fr["name"] != "read" {
				t.Errorf("tool result lost identity: %#v", fr)
			}
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Done\"}]},\"finishReason\":\"STOP\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer server.Close()
	p := NewDeepVProvider(server.URL, &deepVTestTokens{token: "account-token"})
	max := 40000
	req := ai.StreamRequest{Model: ai.Model{ID: "deepseek-flash"}, Messages: []ai.Message{ai.NewTextUserMessage("read a file")}, Tools: []ai.ToolDefinition{{Name: "read", Parameters: map[string]any{"type": "object"}}}, MaxTokens: &max}
	msg, err := collectDeepV(t, p, req)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Text != "Checking" || msg.Thinking != "think" || msg.StopReason != ai.StopReasonToolUse || len(msg.ToolCalls) != 1 {
		t.Fatalf("unexpected first response: %+v", msg)
	}
	if msg.ToolCalls[0].ID != "call-1" || msg.ToolCalls[0].Args != `{"path":"file.txt"}` {
		t.Fatalf("lost tool arguments: %+v", msg.ToolCalls)
	}
	if msg.Usage.InputTokens != 12 || msg.Usage.OutputTokens != 4 || msg.Usage.CachedInputTokens == nil || *msg.Usage.CachedInputTokens != 3 {
		t.Fatalf("lost usage: %+v", msg.Usage)
	}
	req.Messages = append(req.Messages, ai.AssistantMessage{Text: msg.Text, Thinking: msg.Thinking, ThinkingBlocks: msg.ThinkingBlocks, ToolCalls: msg.ToolCalls}, ai.ToolResultMessage{ToolCallID: "call-1", Content: "fixture result"})
	msg, err = collectDeepV(t, p, req)
	if err != nil || msg.Text != "Done" || msg.StopReason != ai.StopReasonStop {
		t.Fatalf("followup: %+v %v", msg, err)
	}
}

func TestDeepVRefreshesOnceAfterUnauthorized(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.Header.Get("Authorization") == "Bearer old-token" {
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer renewed-token" {
			t.Error("request used wrong account")
		}
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	tokens := &deepVTestTokens{token: "old-token"}
	msg, err := collectDeepV(t, NewDeepVProvider(server.URL, tokens), ai.StreamRequest{Model: ai.Model{ID: "fixture"}, Messages: []ai.Message{ai.NewTextUserMessage("hello")}})
	if err != nil || msg.Text != "ok" || attempts.Load() != 2 || tokens.refresh.Load() != 1 {
		t.Fatalf("refresh failed: %+v %v attempts=%d refresh=%d", msg, err, attempts.Load(), tokens.refresh.Load())
	}
}

func TestDeepVRejectsTruncatedOrInvalidToolArguments(t *testing.T) {
	for _, source := range []string{
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n",
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"id\":\"x\",\"name\":\"read\",\"args\":\"{broken\"}}]}}]}\n\ndata: [DONE]\n\n",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, source) }))
		_, err := collectDeepV(t, NewDeepVProvider(server.URL, &deepVTestTokens{token: "t"}), ai.StreamRequest{Model: ai.Model{ID: "fixture"}, Messages: []ai.Message{ai.NewTextUserMessage("hello")}})
		server.Close()
		if err == nil {
			t.Fatal("invalid or truncated stream reported success")
		}
	}
}

func TestDeepVDoesNotForwardCredentialsToRedirect(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := collectDeepV(t, NewDeepVProvider(server.URL, &deepVTestTokens{token: "private"}), ai.StreamRequest{Model: ai.Model{ID: "fixture"}})
	if err == nil || leaked.Load() {
		t.Fatal("redirect forwarded credentials or reported success")
	}
}

func TestDeepVPreservesImagesParallelToolResultsAndTemperature(t *testing.T) {
	temperature := 0.0
	messages := []ai.Message{
		ai.UserMessage{Content: []ai.ContentBlock{{Type: "image", Image: &ai.ImageBlock{MediaType: "image/png", Data: []byte{1, 2, 3}}}, {Type: "image", Image: &ai.ImageBlock{URL: "https://example.test/photo.png"}}}},
		ai.AssistantMessage{ToolCalls: []ai.ToolCall{{ID: "one", Name: "screen", Args: `{}`}, {ID: "two", Name: "read", Args: `{"path":"a"}`}}},
		ai.ToolResultMessage{ToolCallID: "one", Content: "screen", Images: []ai.ImageBlock{{MediaType: "image/png", Data: []byte{4, 5, 6}}}},
		ai.ToolResultMessage{ToolCallID: "two", Content: "file"},
	}
	inner := NewAnthropicProvider("", "")
	wire, err := deepVFromAnthropic(inner.buildRequest(ai.StreamRequest{Model: ai.Model{ID: "fixture"}, Messages: messages, Temperature: &temperature}, true))
	if err != nil {
		t.Fatal(err)
	}
	if wire.Config.Temperature == nil || *wire.Config.Temperature != 0 {
		t.Fatal("zero temperature was omitted")
	}
	if len(wire.Contents) != 3 || len(wire.Contents[0].Parts) != 2 {
		t.Fatalf("missing input images: %+v", wire.Contents)
	}
	if wire.Contents[0].Parts[0].InlineData.Data != "AQID" || wire.Contents[0].Parts[1].FileData.Mime != "image/png" {
		t.Fatal("input image conversion failed")
	}
	parts := wire.Contents[2].Parts
	if len(parts) != 3 || parts[0].FunctionResponse.Name != "screen" || parts[1].InlineData.Data != "BAUG" || parts[2].FunctionResponse.Name != "read" {
		t.Fatalf("parallel tool results lost ordering or images: %+v", parts)
	}
	source := inner.buildRequest(ai.StreamRequest{Model: ai.Model{ID: "claude-haiku-5-5"}, Temperature: &temperature}, true)
	if special, err := deepVFromAnthropic(source); err != nil || special.Config.Temperature != nil {
		t.Fatal("Haiku temperature was not omitted")
	}
}

func TestDeepVToolResultsAndSteeringStayInOneTurn(t *testing.T) {
	messages := []ai.Message{
		ai.AssistantMessage{ToolCalls: []ai.ToolCall{{ID: "one", Name: "read", Args: `{}`}, {ID: "two", Name: "read", Args: `{}`}}},
		ai.ToolResultMessage{ToolCallID: "one", Content: "first"},
		ai.ToolResultMessage{ToolCallID: "two", Content: "second"},
		ai.NewTextUserMessage("loop reminder"),
		ai.NewTextUserMessage("user correction"),
	}
	inner := NewAnthropicProvider("", "")
	wire, err := deepVFromAnthropic(inner.buildRequest(ai.StreamRequest{Model: ai.Model{ID: "fixture"}, Messages: messages}, true))
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Contents) != 2 || wire.Contents[1].Role != "user" {
		t.Fatalf("tool results and steering were split into different user turns: %+v", wire.Contents)
	}
	parts := wire.Contents[1].Parts
	if len(parts) != 4 || parts[0].FunctionResponse == nil || parts[1].FunctionResponse == nil || parts[2].Text != "loop reminder" || parts[3].Text != "user correction" {
		t.Fatalf("tool results or steering were lost or reordered: %+v", parts)
	}
}
