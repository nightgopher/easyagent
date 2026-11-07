package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/compaction"
	"github.com/nightgopher/easyagent/sdk/session"
	"github.com/nightgopher/easyagent/sdk/tools"
	"github.com/stretchr/testify/require"
)

// A deterministic model fixture keeps the real agent loop and file tools in
// the test. It changes course only after it receives the stall reminder.
type codingProgressProvider struct {
	path           string
	calls          int
	reminder       bool
	readsAvailable int
	editSucceeded  bool
}

func (*codingProgressProvider) Name() string { return "coding-progress-fixture" }
func (p *codingProgressProvider) StreamSimple(ctx context.Context, req ai.SimpleStreamRequest) (*ai.EventStream, error) {
	return p.Stream(ctx, ai.StreamRequest{Model: req.Model, Messages: req.Messages})
}
func (p *codingProgressProvider) Stream(ctx context.Context, req ai.StreamRequest) (*ai.EventStream, error) {
	p.calls++
	p.readsAvailable = 0
	for _, raw := range req.Messages {
		switch m := raw.(type) {
		case ai.UserMessage:
			for _, block := range m.Content {
				p.reminder = p.reminder || strings.Contains(block.Text, "file reads overlap")
			}
		case ai.ToolResultMessage:
			if strings.HasPrefix(m.ToolCallID, "read-") && !m.IsError && !strings.Contains(m.Content, "older tool result cleared") {
				p.readsAvailable++
			}
			if m.ToolCallID == "edit" && !m.IsError {
				p.editSucceeded = true
			}
		}
	}
	msg := ai.StreamAssistantMessage{Text: "done", StopReason: ai.StopReasonStop}
	if !p.editSucceeded {
		name, id := "read", fmt.Sprintf("read-%d", p.calls)
		args := map[string]any{"path": p.path, "offset": (p.calls % 3) * 100, "limit": 300}
		if p.reminder {
			name, id = "edit", "edit"
			args = map[string]any{"path": p.path, "old_string": "return 1", "new_string": "return 2"}
		}
		encoded, _ := json.Marshal(args)
		msg = ai.StreamAssistantMessage{StopReason: ai.StopReasonToolUse, ToolCalls: []ai.ToolCall{{ID: id, Name: name, Args: string(encoded)}}}
	}
	stream := ai.NewEventStream(2)
	go func() {
		defer stream.Close()
		_ = stream.Push(ctx, ai.EventDone{Reason: msg.StopReason, Message: msg})
		stream.SetResult(msg, nil)
	}()
	return stream, nil
}

func TestCodingAgentMovesFromRepeatedReadsToRealEdit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	original := "package main\n" + strings.Repeat("// line\n", 350) + "func value() int { return 1 }\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
	storage := session.NewJSONLStorage(filepath.Join(t.TempDir(), "session.jsonl"))
	require.NoError(t, storage.Init())
	defer storage.Close()
	tracker := tools.NewReadTracker()
	p := &codingProgressProvider{path: path}
	registry := providers.NewRegistry()
	registry.Register(p)
	ag := agent.New(agent.Options{
		Registry: registry, Model: ai.Model{Provider: p.Name(), ContextWindow: 128000},
		System: strings.Repeat("context ", 37500), Session: session.New(storage), MaxTurns: 20,
		Tools: []agent.Tool{tools.NewReadTool(tools.WithReadWorkspace(dir), tools.WithReadTracker(tracker)),
			tools.NewEditTool(tools.WithEditWorkspace(dir), tools.WithEditReadTracker(tracker))},
		CompactionSettings: compaction.DefaultSettings(), LoopDetectSettings: agent.DefaultLoopDetectSettings(),
	})
	result, err := ag.Prompt(ctx, ai.NewTextUserMessage("Change value to return 2"))
	require.NoError(t, err)
	require.True(t, p.reminder)
	require.True(t, p.editSucceeded)
	require.GreaterOrEqual(t, p.readsAvailable, 12, "recent file reads remain available to the model")
	require.Equal(t, "done", result.Text)
	updated, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(updated), "return 2")
}
