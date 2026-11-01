package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestModelRetryProjectionAndRecovery(t *testing.T) {
	now := time.Now()
	retry := &modelRetry{Attempt: 2, MaxAttempts: 3, RetryAt: now.Add(10 * time.Second).UnixMilli(), Reason: "rate_limit"}
	s := &session{}
	s.reduce(envelope{Type: "accepted", RunID: "r", State: "running", Prompt: "write once"})
	s.reduce(envelope{Type: "event", RunID: "r", Event: &agentEvent{Type: "tool_start", ToolID: "write-1", ToolName: "write"}})
	s.reduce(envelope{Type: "event", RunID: "r", Event: &agentEvent{Type: "tool_end", ToolID: "write-1", Result: "saved"}})
	s.reduce(envelope{Type: "event", RunID: "r", Event: &agentEvent{Type: "model_retry", Retry: retry}})
	if !s.Running || s.statusText() != "等待重试" || len(s.Messages) != 2 || s.Messages[1].Status != "完成" {
		t.Fatal("backoff lost run state or completed tools")
	}
	if label := retry.label(now); label != "模型服务暂时限流，10 秒后自动重试（2/3）" {
		t.Fatal(label)
	}
	if label := retry.label(now.Add(11 * time.Second)); label != "正在重新请求模型（2/3）" {
		t.Fatal(label)
	}

	raw, _ := json.Marshal(agentEvent{Type: "model_retry", Retry: retry})
	s.reduce(envelope{Type: "snapshot", Run: &runInfo{ID: "r", State: "running"}, Events: []json.RawMessage{raw}})
	if s.ModelRetry == nil || s.ModelRetry.Attempt != 2 {
		t.Fatal("snapshot lost waiting status")
	}
	s.reduce(envelope{Type: "event", RunID: "r", Event: &agentEvent{Type: "text_delta", Text: "done"}})
	if s.ModelRetry != nil || !s.Running {
		t.Fatal("content did not clear retry state")
	}
	s.reduce(envelope{Type: "event", RunID: "r", Event: &agentEvent{Type: "model_retry", Retry: retry}})
	s.reduce(envelope{Type: "status", RunID: "r", State: "cancelled"})
	if s.ModelRetry != nil || s.Running {
		t.Fatal("cancel left a retry active")
	}
	s.reduce(envelope{Type: "accepted", RunID: "new", State: "running"})
	s.reduce(envelope{Type: "event", RunID: "r", Event: &agentEvent{Type: "model_retry", Retry: retry}})
	if s.ModelRetry != nil {
		t.Fatal("stale run changed the current retry state")
	}
	if classifyRecovery(`API error 429: {"error":{"code":"insufficient_quota"}}`).Kind != "quota" {
		t.Fatal("permanent quota misclassified")
	}
	if strings.Contains(classifyRecovery("API error 429").Message, "任务不会自动重试") {
		t.Fatal("misleading recovery message")
	}
}

func TestModelRetryVisibleAndCancellable(t *testing.T) {
	a := alignmentPreview(t)
	a.Live.Connected = true
	s := a.current()
	s.Messages = []*message{{User: true, Text: "写入文件并汇报", RunID: "r"}, {Kind: "tool", Tool: "write", CallID: "write-1", RunID: "r", Text: "result.txt", Status: "完成", Result: "文件已保存"}}
	s.Run, s.Running = &runInfo{ID: "r", State: "running"}, true
	s.ModelRetry = &modelRetry{Attempt: 1, MaxAttempts: 3, RetryAt: time.Now().Add(time.Minute).UnixMilli(), Reason: "rate_limit"}
	tt := ui.NewTester(a.View, 1000, 800)
	if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
		saveSnapshot(t, tt, filepath.Join(dir, "model-retry.png"))
	}
	if !tt.HasText("等待重试") || !tt.HasText("已完成的工具操作会保留。可随时停止重试。") {
		t.Fatalf("retry status is not visible: heading=%v, detail=%v", tt.HasText("等待重试"), tt.HasText("已完成的工具操作会保留。可随时停止重试。"))
	}
}
