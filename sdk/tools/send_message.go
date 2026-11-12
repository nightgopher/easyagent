package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nightgopher/easyagent/sdk/agent"
)

// SendMessageTool delivers a proactive message from the agent to the user
// mid-task; the task keeps running after delivery. The tool itself only
// returns the message as ToolResult.UserFacing — UI layers (feishu bridge,
// TUI) special-case the tool name on tool_end events and surface it as a
// standalone chat message, the same way ask_user_question is special-cased.
type SendMessageTool struct{}

type SendMessageParams struct {
	Message string `json:"message"`
}

func NewSendMessageTool() *SendMessageTool {
	return &SendMessageTool{}
}

func (t *SendMessageTool) Name() string { return "send_message" }

func (t *SendMessageTool) Description() string {
	return "Send a proactive message to the user right now while you keep working on the current task. " +
		"Use it at meaningful checkpoints of a long task: a milestone finished, a blocker found (state your workaround), " +
		"or a plan change the user should know about. The message is delivered as a standalone chat message and " +
		"the task continues; the user may not answer immediately. " +
		"Do not use it for routine narration or tool-by-tool progress, and do not repeat what you already sent."
}

func (t *SendMessageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{
				"type":        "string",
				"description": "The message to deliver to the user. Plain text, concise, self-contained.",
			},
		},
		"required": []string{"message"},
	}
}

func (t *SendMessageTool) Validate(raw json.RawMessage) (json.RawMessage, error) {
	var params SendMessageParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.Message) == "" {
		return nil, fmt.Errorf("message is empty")
	}
	return json.Marshal(params)
}

// IsConcurrencySafe implements agent.ConcurrencySafeChecker.
func (t *SendMessageTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *SendMessageTool) Execute(_ context.Context, raw json.RawMessage, _ func(agent.PartialResult)) (agent.ToolResult, error) {
	var params SendMessageParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	message := strings.TrimSpace(params.Message)
	if message == "" {
		return agent.ToolResult{IsError: true}, fmt.Errorf("message is empty")
	}
	// UI 按 UserFacing 送达用户；Content 回给 LLM，不含正文（正文已在 tool call
	// 参数里，避免上下文重复）。
	return agent.ToolResult{
		Content:    "Message delivered to user.",
		UserFacing: message,
	}, nil
}
