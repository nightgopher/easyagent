package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAIMessageTextPlain(t *testing.T) {
	m := openaiMessage{Role: "user", Content: "你好"}
	if got := m.text(); got != "你好" {
		t.Fatalf("text() = %q", got)
	}
}

func TestOpenAIMessageTextMultimodalParts(t *testing.T) {
	m := openaiMessage{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "第一段"},
		map[string]any{"type": "image_url"},
		map[string]any{"type": "text", "text": "第二段"},
		map[string]any{"type": "text"},
	}}
	if got := m.text(); got != "第一段第二段" {
		t.Fatalf("text() = %q", got)
	}
}

func TestOpenAIMessageTextUnexpected(t *testing.T) {
	if got := (openaiMessage{Content: 42}).text(); got != "" {
		t.Fatalf("text() = %q, want empty", got)
	}
}

func TestLastUserTextFindsLatestNonEmpty(t *testing.T) {
	msgs := []openaiMessage{
		{Role: "user", Content: "第一条"},
		{Role: "assistant", Content: "回答"},
		{Role: "user", Content: ""},
		{Role: "user", Content: "最新问题"},
	}
	got, ok := lastUserText(msgs)
	if !ok || got != "最新问题" {
		t.Fatalf("lastUserText = %q, %v", got, ok)
	}
}

func TestLastUserTextNone(t *testing.T) {
	msgs := []openaiMessage{{Role: "assistant", Content: "只有回答"}}
	if _, ok := lastUserText(msgs); ok {
		t.Fatal("expected ok=false when there is no user message")
	}
}

func TestOpenAIChatRequestParsesTools(t *testing.T) {
	raw := `{"model":"coding","messages":[{"role":"user","content":"hi"}],
		"stream":false,"tools":[{"type":"function","function":{"name":"job.runs","description":"查任务","parameters":{}}}]}`
	var req openaiChatRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "job.runs" {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if req.Messages[0].text() != "hi" {
		t.Fatalf("message text = %q", req.Messages[0].text())
	}
}

func TestVoicePromptUsesLastQuestionAndOmitsToolsWhenEmpty(t *testing.T) {
	got := voicePrompt([]openaiMessage{{Role: "user", Content: "现在几点"}}, nil)
	if got == "" || !strings.Contains(got, "现在几点") {
		t.Fatalf("voicePrompt should carry the last question, got %q", got)
	}
}

func TestOpenAIChatResponseMarshal(t *testing.T) {
	stop := "stop"
	resp := openaiChatResponse{ID: "chatcmpl-1", Object: "chat.completion", Created: 1, Model: "coding",
		Choices: []openaiChatChoice{{Index: 0, Message: &openaiChatText{Role: "assistant", Content: "好"}, FinishReason: &stop}}}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := back["choices"]; !has {
		t.Fatal("choices missing")
	}
}
