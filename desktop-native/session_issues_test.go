package main

import (
	"testing"
)

func TestWaitingPlaceholderTransitionsAndForkAnchors(t *testing.T) {
	for _, kind := range []string{"text_delta", "thinking_delta", "tool_start", "error"} {
		t.Run(kind, func(t *testing.T) {
			s := &session{}
			s.reduce(envelope{Type: "accepted", RunID: "run", State: "running", Prompt: "hello"})
			if len(s.Messages) != 2 || s.Messages[1].Text != "" || s.Messages[1].WaitingSince.IsZero() {
				t.Fatal("missing immediate placeholder")
			}
			s.reduce(envelope{Type: "event", RunID: "run", Event: &agentEvent{Type: kind, Text: "delta", ToolID: "call", ToolName: "read", ErrorText: "failed"}})
			if kind == "text_delta" || kind == "thinking_delta" {
				if len(s.Messages) != 2 || s.Messages[1].Text != "delta" {
					t.Fatal("placeholder was not reused")
				}
			}
			s.reduce(envelope{Type: "status", RunID: "run", State: "failed", Run: &runInfo{ID: "run", State: "failed"}})
			for _, m := range s.Messages {
				if !m.User && m.Kind == "" && m.Text == "" {
					t.Fatal("empty assistant left behind")
				}
			}
		})
	}
	s := &session{Running: true, Run: &runInfo{ID: "run", State: "running"}}
	historical := &message{EntryID: "old"}
	current := &message{EntryID: "new", RunID: "run"}
	if !s.canForkMessage(historical) || s.canForkMessage(current) {
		t.Fatal("busy guard did not distinguish history from current run")
	}
	s.Messages = []*message{{Text: "plan", RunID: "run"}, {Kind: "tool", CallID: "call", RunID: "run"}, {Text: "partial", RunID: "run"}}
	s.syncAnchors([]storedMessage{{Role: "assistant", Content: "plan", EntryID: "assistant"}, {Role: "tool", ToolID: "call", EntryID: "result"}, {Role: "assistant", Content: "partial", EntryID: "partial"}})
	for _, m := range s.Messages {
		if m.EntryID == "" {
			t.Fatal("tool/partial anchor missing")
		}
	}
}
func TestReasoningSettingsValidation(t *testing.T) {
	cfg := modelConfig{Mode: "override", Provider: "anthropic", BaseURL: "http://localhost:4001", Model: "claude", Thinking: "enabled", ThinkingBudget: "2048", StreamIdleTimeout: "5m"}
	if _, err := validatedModel(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ThinkingBudget = "1"
	if _, err := validatedModel(cfg); err == nil {
		t.Fatal("invalid budget accepted")
	}
	cfg.ThinkingBudget = "2048"
	cfg.StreamIdleTimeout = "0s"
	if _, err := validatedModel(cfg); err == nil {
		t.Fatal("invalid timeout accepted")
	}
}
