package computer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func normalize(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	normalized, err := validateActions(json.RawMessage(raw))
	require.NoError(t, err)
	return normalized
}

func TestRequiresConfirmationOnlyForControl(t *testing.T) {
	tool := NewTool(t.TempDir())

	// 只读动作不需要确认
	_, ok := tool.RequiresConfirmation(json.RawMessage(`{"bundle_id":"test.editor","pid":100,"window_id":200,"action":"screenshot"}`))
	require.False(t, ok)
	_, ok = tool.RequiresConfirmation(json.RawMessage(`{"bundle_id":"test.editor","pid":100,"window_id":200,"action":"get_app_state"}`))
	require.False(t, ok)

	// 控制批需要确认，描述包含每一步
	desc, ok := tool.RequiresConfirmation(json.RawMessage(
		`{"bundle_id":"test.editor","pid":100,"window_id":200,"action":"perform_action","actions":[{"type":"click","x":10,"y":20},{"type":"key","key":"cmd+s"}]}`))
	require.True(t, ok)
	require.Contains(t, desc, "电脑控制")
	require.Contains(t, desc, "左键点击 (10, 20)")
	require.Contains(t, desc, "按键 cmd+s")
	require.Contains(t, desc, "依次执行 2 个动作")
}

func TestHighRiskFlagging(t *testing.T) {
	tool := NewTool(t.TempDir())

	// cmd+q 高危 → 描述带 ⚠️
	desc, ok := tool.RequiresConfirmation(json.RawMessage(
		`{"bundle_id":"test.editor","pid":100,"window_id":200,"action":"perform_action","actions":[{"type":"key","key":"cmd+q"}]}`))
	require.True(t, ok)
	require.Contains(t, desc, "⚠️ 高风险")

	// 普通按键不高危
	desc, _ = tool.RequiresConfirmation(json.RawMessage(
		`{"bundle_id":"test.editor","pid":100,"window_id":200,"action":"perform_action","actions":[{"type":"key","key":"cmd+s"}]}`))
	require.NotContains(t, desc, "⚠️")
}
