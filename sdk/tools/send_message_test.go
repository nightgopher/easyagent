package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/stretchr/testify/require"
)

func TestSendMessageToolValidation(t *testing.T) {
	tool := NewSendMessageTool()
	require.Equal(t, "send_message", tool.Name())

	valid, err := tool.Validate(json.RawMessage(`{"message":"第一步完成，继续跑测试"}`))
	require.NoError(t, err)

	_, err = tool.Validate(json.RawMessage(`{"message":"  "}`))
	require.Error(t, err, "blank message must be rejected")

	_, err = tool.Validate(json.RawMessage(`{}`))
	require.Error(t, err, "missing message must be rejected")

	_, err = tool.Validate(json.RawMessage(`not json`))
	require.Error(t, err)

	require.True(t, tool.IsConcurrencySafe(valid))
}

func TestSendMessageToolExecuteDeliversViaUserFacing(t *testing.T) {
	tool := NewSendMessageTool()
	raw, err := tool.Validate(json.RawMessage(`{"message":"遇到一个报错，我改用备选方案继续"}`))
	require.NoError(t, err)

	result, err := tool.Execute(context.Background(), raw, func(agent.PartialResult) {})
	require.NoError(t, err)
	require.False(t, result.IsError)
	// UI 按 UserFacing 送达用户；Content 只回执给 LLM，不重复正文。
	require.Equal(t, "遇到一个报错，我改用备选方案继续", result.UserFacing)
	require.Equal(t, "Message delivered to user.", result.Content)
	require.Equal(t, result.UserFacing, result.DisplayText())
}

func TestSendMessageToolExecuteRejectsBlank(t *testing.T) {
	tool := NewSendMessageTool()
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"message":" "}`), func(agent.PartialResult) {})
	require.Error(t, err)
	require.True(t, result.IsError)
}
