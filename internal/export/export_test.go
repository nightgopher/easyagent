package export

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
)

func sampleMessages() []ai.Message {
	return []ai.Message{
		ai.NewTextUserMessage("帮我看看 main.go"),
		ai.AssistantMessage{
			Text: "好的,我先读取文件。",
			ToolCalls: []ai.ToolCall{
				{ID: "call_1", Name: "read_file", Args: `{"path":"main.go"}`},
			},
			StopReason: ai.StopReasonToolUse,
		},
		ai.ToolResultMessage{
			ToolCallID: "call_1",
			Content:    "package main",
			DurationMS: 120,
		},
		ai.AssistantMessage{Text: "文件内容是 hello world 程序。"},
	}
}

func TestRenderMarkdownIncludesTools(t *testing.T) {
	result, err := Render(sampleMessages(), Options{
		Format: FormatMarkdown, IncludeTools: true, SessionID: "sess_1234567890", Title: "调试:main.go",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := string(result.Content)
	for _, want := range []string{"调试:main.go", "## 👤 用户", "帮我看看 main.go", "## 🤖 助手", "read_file", "package main", "包含工具调用:是"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown 缺少 %q", want)
		}
	}
	if !strings.Contains(result.Filename, "-1234567890") && !strings.Contains(result.Filename, "90") {
		t.Errorf("文件名应包含会话 id 尾部,got %q", result.Filename)
	}
	if !strings.HasSuffix(result.Filename, ".md") {
		t.Errorf("文件名后缀应为 .md,got %q", result.Filename)
	}
}

func TestRenderMarkdownExcludesTools(t *testing.T) {
	result, err := Render(sampleMessages(), Options{
		Format: FormatMarkdown, IncludeTools: false, SessionID: "sess_abc", Title: "t",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := string(result.Content)
	if strings.Contains(out, "read_file") || strings.Contains(out, "package main") || strings.Contains(out, "🔧 工具调用") || strings.Contains(out, "↩︎ 工具结果") {
		t.Errorf("不含工具模式不应出现工具内容")
	}
	if !strings.Contains(out, "文件内容是 hello world 程序。") {
		t.Errorf("助手文本应保留")
	}
}

func TestRenderJSON(t *testing.T) {
	result, err := Render(sampleMessages(), Options{
		Format: FormatJSON, IncludeTools: true, SessionID: "sess_x", Title: "t",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var payload struct {
		Session  map[string]any `json:"session"`
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(result.Content, &payload); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if payload.Session["id"] != "sess_x" {
		t.Errorf("session.id = %v", payload.Session["id"])
	}
	if len(payload.Messages) != 4 {
		t.Fatalf("应有 4 条消息,got %d", len(payload.Messages))
	}
	if payload.Messages[2].ToolCallID != "call_1" {
		t.Errorf("tool 结果应带 tool_call_id")
	}
	if !strings.HasSuffix(result.Filename, ".json") {
		t.Errorf("文件名后缀应为 .json,got %q", result.Filename)
	}
}

func TestRenderText(t *testing.T) {
	result, err := Render(sampleMessages(), Options{
		Format: FormatText, IncludeTools: true, SessionID: "s", Title: "t",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(result.Content), "[用户] 帮我看看 main.go") {
		t.Errorf("纯文本格式不符")
	}
}

func TestRenderInvalidFormat(t *testing.T) {
	if _, err := Render(sampleMessages(), Options{Format: "html"}); err == nil {
		t.Fatal("非法格式应返回错误")
	}
}

func TestFilenameSanitized(t *testing.T) {
	result, err := Render(nil, Options{Format: FormatMarkdown, SessionID: "sess_abcd1234", Title: "a/b\\c:危险*标题?"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.ContainsAny(result.Filename, "/\\:*?") {
		t.Errorf("文件名不应含非法字符: %q", result.Filename)
	}
}
