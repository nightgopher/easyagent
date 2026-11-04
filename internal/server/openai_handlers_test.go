package server

import (
	"encoding/json"
	"testing"
)

// TestOpenAIChatRequestParsesTools pins the wire contract of the
// OpenAI-compatible chat endpoint: external clients (Codex 等) rely on the
// tools array surviving unmarshaling.
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
