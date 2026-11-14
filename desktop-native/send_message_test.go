package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestNativeSendMessageBroadcast：tool_end 时 send_message 正文作为独立气泡
// 紧贴工具行上屏（与 TUI 行为一致）；同一 CallID 的回放不叠加。
func TestNativeSendMessageBroadcast(t *testing.T) {
	s := &session{Run: &runInfo{ID: "run"}}
	s.event(agentEvent{Type: "tool_start", ToolID: "sm1", ToolName: "send_message", Args: map[string]any{"message": "阶段一完成"}})
	s.event(agentEvent{Type: "tool_end", ToolID: "sm1", ToolName: "send_message", Result: "阶段一完成，继续下一阶段"})
	if len(s.Messages) != 2 {
		t.Fatalf("tool row + broadcast expected: %d messages", len(s.Messages))
	}
	if s.Messages[0].Kind != "tool" || s.Messages[0].Tool != "send_message" {
		t.Fatalf("tool row must stay: %+v", s.Messages[0])
	}
	b := s.Messages[1]
	if b.Kind != "broadcast" || b.Text != "阶段一完成，继续下一阶段" || b.CallID != "sm1" {
		t.Fatalf("broadcast bubble wrong: %+v", b)
	}
	s.event(agentEvent{Type: "tool_end", ToolID: "sm1", ToolName: "send_message", Result: "阶段一完成，继续下一阶段"})
	if len(s.Messages) != 2 {
		t.Fatal("replayed tool_end must not duplicate the broadcast")
	}
}

// TestNativeSendMessageBroadcastSuppressed：失败结果与其他工具不产生气泡。
func TestNativeSendMessageBroadcastSuppressed(t *testing.T) {
	s := &session{Run: &runInfo{ID: "run"}}
	s.event(agentEvent{Type: "tool_start", ToolID: "sm2", ToolName: "send_message"})
	s.event(agentEvent{Type: "tool_end", ToolID: "sm2", ToolName: "send_message", Result: "x", Error: true})
	if len(s.Messages) != 1 || s.Messages[0].Kind != "tool" {
		t.Fatalf("failed broadcast must not create a bubble: %+v", s.Messages)
	}

	s2 := &session{Run: &runInfo{ID: "run"}}
	s2.event(agentEvent{Type: "tool_start", ToolID: "r1", ToolName: "read", Args: map[string]any{"path": "x"}})
	s2.event(agentEvent{Type: "tool_end", ToolID: "r1", ToolName: "read", Result: "ok"})
	if len(s2.Messages) != 1 || s2.Messages[0].Kind != "tool" {
		t.Fatalf("non-broadcast tools must not create a bubble: %+v", s2.Messages)
	}
}

// TestNativeSendMessageBroadcastFromHistory：历史会话重载时从 assistant 记录的
// tool_calls args 重导播报气泡，与运行时投影一致。
func TestNativeSendMessageBroadcastFromHistory(t *testing.T) {
	s := &session{}
	s.history([]storedMessage{
		{Role: "user", Content: "开始干活"},
		{Role: "assistant", Content: "", StopReason: "tool_calls", ToolCalls: []struct {
			ID, Name string
			Args     any
		}{{ID: "sm9", Name: "send_message", Args: map[string]any{"message": "里程碑达成"}}}},
		{Role: "tool", ToolID: "sm9", Content: "Message delivered to user."},
		{Role: "assistant", Content: "完成", StopReason: "stop"},
	})
	if len(s.Messages) != 4 {
		names := make([]string, 0, len(s.Messages))
		for _, m := range s.Messages {
			names = append(names, m.Kind+"/"+m.Tool)
		}
		t.Fatalf("user/tool/broadcast/assistant expected: %d messages %v", len(s.Messages), names)
	}
	if s.Messages[1].Kind != "tool" || s.Messages[1].Tool != "send_message" {
		t.Fatalf("tool row must stay: %+v", s.Messages[1])
	}
	b := s.Messages[2]
	if b.Kind != "broadcast" || b.Text != "里程碑达成" || b.CallID != "sm9" {
		t.Fatalf("history broadcast wrong: %+v", b)
	}
}

// TestNativeSendMessageBubbleRender：播报气泡带区分性标签与正文。
func TestNativeSendMessageBubbleRender(t *testing.T) {
	a := newPreview()
	a.Live = &liveState{Workbench: newNativeWorkbench()}
	s := a.current()
	s.Messages = []*message{
		{User: true, Text: "开始干活"},
		{Kind: "broadcast", CallID: "sm1", Text: "第一阶段完成"},
	}
	s.Run = &runInfo{ID: "r1", State: "completed"}
	s.Seq = 2
	tt := ui.NewTester(a.View, 1240, 820)
	tt.Frame()
	if !tt.HasText("Agent 播报") || !tt.HasText("第一阶段完成") {
		t.Fatal("broadcast bubble not rendered with label and body")
	}
}
