package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestWorkflowAppearsInTypedCommandCompletion(t *testing.T) {
	a := alignmentPreview(t)
	a.current().AgentID = "agent-steward"
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "消息输入")
	tt.Type("/wor")
	if !a.SlashMenu || !tt.HasText("/workflow · 多角色动态工作流") {
		t.Fatal("workflow completion was not shown")
	}
	tt.Key(0, ui.KeyTab)
	if a.current().Draft != "/workflow " {
		t.Fatalf("unexpected completion: %q", a.current().Draft)
	}
}
