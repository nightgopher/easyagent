package main

import (
	"encoding/json"
	"testing"
)

func TestStoppedRunClosesUnfinishedToolStatus(t *testing.T) {
	s := &session{}
	s.reduce(envelope{Type: "accepted", RunID: "run", State: "running"})
	s.reduce(envelope{Type: "event", RunID: "run", Event: &agentEvent{Type: "tool_start", ToolName: "create_workflow", ToolID: "call"}})
	s.reduce(envelope{Type: "status", RunID: "run", State: "cancelled"})
	for _, m := range s.Messages {
		if m.Tool == "create_workflow" {
			if m.Status != "未完成" {
				t.Fatalf("tool still %q", m.Status)
			}
			return
		}
	}
	t.Fatal("workflow tool missing")
}

func TestStoppedSnapshotClosesToolAndKeepsWorkflowID(t *testing.T) {
	s := &session{}
	s.reduce(envelope{Type: "snapshot", Run: &runInfo{ID: "run", State: "cancelled"}, Events: []json.RawMessage{
		json.RawMessage(`{"type":"tool_start","tool_name":"create_workflow","tool_call_id":"call"}`),
		json.RawMessage(`{"type":"tool_update","tool_name":"create_workflow","tool_call_id":"call","partial_result":{"content":"工作流 dw-0123456789abcdef01234567 · running"}}`),
	}})
	for _, m := range s.Messages {
		if m.Tool == "create_workflow" {
			if m.Status != "未完成" || conversationWorkflowID(m) == "" {
				t.Fatalf("lost interrupted status or id: %+v", m)
			}
			return
		}
	}
	t.Fatal("workflow tool missing")
}
