package tui

import (
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/nightgopher/easyagent/sdk/slashcmd"
)

// send_message 的 tool_end 以独立 assistant 气泡上屏；错误结果不上屏。
func TestSendMessageToolEndAppendsBroadcast(t *testing.T) {
	m := New(&runtime.AgentSession{}, slashcmd.NewRegistry(), false)
	m.streamID = 1

	m.Update(streamEventMsg{1, ToolEndMsg{Name: "send_message", Result: "阶段性进展：构建通过"}})
	if countBroadcasts(m) != 1 {
		t.Fatal("broadcast bubble missing")
	}

	// 错误结果不是播报，不上屏。
	m.Update(streamEventMsg{1, ToolEndMsg{Name: "send_message", Result: "x", IsError: true}})
	if countBroadcasts(m) != 1 {
		t.Fatal("error result must not become a broadcast bubble")
	}

	// 其他工具的 tool_end 不产生播报。
	m.Update(streamEventMsg{1, ToolEndMsg{Name: "write", Result: "阶段性进展：构建通过"}})
	if countBroadcasts(m) != 1 {
		t.Fatal("non send_message tool must not produce a broadcast bubble")
	}
}

func countBroadcasts(m *TuiModel) int {
	count := 0
	for _, msg := range m.messages {
		if msg.Role == "assistant" && strings.Contains(msg.Content, "阶段性进展：构建通过") {
			count++
		}
	}
	return count
}
