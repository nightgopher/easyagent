package stewardtools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/stretchr/testify/require"
)

func toolParams(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	if raw == "" {
		raw = "{}"
	}
	return json.RawMessage(raw)
}

func TestHostSessionCreateValidation(t *testing.T) {
	tool := Tool{dispatch: func(context.Context, DispatchRequest) (string, bool, error) { return "", false, nil }}

	valid := []string{
		``,
		`{"title":"查日志","cwd":"/tmp","prompt":"排查 easygateway 日志"}`,
		`{"application":"business","agent_id":"agent-abc"}`,
	}
	for _, params := range valid {
		_, err := tool.Validate(toolParams(t, params))
		require.NoError(t, err, params)
	}

	invalid := []string{
		`{"application":"kb"}`,                                  // 未注册应用
		`{"application":"business"}`,                            // business 缺 agent_id
		`{"agent_id":"agent-steward"}`,                          // 管家不能派生管家
		`{"title":"over-the-limit-over-the-limit-over-the-limit-over-the-limit-over-the-limit-over-the-limit-over-the-limit-over-the-limit-over-the-limit-over-the-limit-over-the-limit-over-the-limit-over-the-limit-over-the-limit-over-the-limit"}`,
		`{"unexpected":true}`,
		`[]`,
	}
	for _, params := range invalid {
		_, err := tool.Validate(toolParams(t, params))
		require.Error(t, err, params)
	}
}

func TestHostSessionCreateExecute(t *testing.T) {
	var got DispatchRequest
	tool := Tool{dispatch: func(_ context.Context, req DispatchRequest) (string, bool, error) {
		got = req
		return "sess_123", true, nil
	}}
	result, err := tool.Execute(context.Background(), toolParams(t, `{"title":"查日志","cwd":"/tmp/repo","prompt":"排查日志问题"}`), func(agent.PartialResult) {})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, "查日志", got.Title)
	require.Equal(t, "/tmp/repo", got.Cwd)
	require.Equal(t, "排查日志问题", got.Prompt)
	require.Equal(t, "", got.Application, "application 留空由 dispatcher 默认 coding")
	require.Contains(t, result.Content, "sess_123")
	require.Contains(t, result.Content, "easyagent://session/sess_123")
	details, ok := result.Details.(json.RawMessage)
	require.True(t, ok)
	require.Contains(t, string(details), `"queued":true`)
}

func TestHostSessionCreateRejectsStewardTarget(t *testing.T) {
	tool := Tool{dispatch: func(context.Context, DispatchRequest) (string, bool, error) {
		return "", false, nil
	}}
	_, err := tool.Validate(toolParams(t, `{"agent_id":"agent-steward"}`))
	require.ErrorContains(t, err, "cannot spawn another steward")
}

func TestHostSessionCreateExecuteReportsDispatchFailure(t *testing.T) {
	tool := Tool{dispatch: func(context.Context, DispatchRequest) (string, bool, error) {
		return "", false, errors.New("agent not found")
	}}
	result, err := tool.Execute(context.Background(), toolParams(t, `{"application":"business","agent_id":"agent-missing"}`), func(agent.PartialResult) {})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.Content, "agent not found")
}

func TestToolsNilWithoutDispatcher(t *testing.T) {
	require.Nil(t, Tools(nil))
	tools := Tools(func(context.Context, DispatchRequest) (string, bool, error) { return "", false, nil })
	require.Len(t, tools, 1)
	require.Equal(t, ToolName, tools[0].Name())
}
