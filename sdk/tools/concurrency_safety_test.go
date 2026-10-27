package tools

import (
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/stretchr/testify/assert"
)

// 编译期断言：只读工具实现 ConcurrencySafeChecker
var _ agent.ConcurrencySafeChecker = (*ReadTool)(nil)
var _ agent.ConcurrencySafeChecker = (*GrepTool)(nil)
var _ agent.ConcurrencySafeChecker = (*FindTool)(nil)
var _ agent.ConcurrencySafeChecker = (*LsTool)(nil)
var _ agent.ConcurrencySafeChecker = (*WebFetchTool)(nil)
var _ agent.ConcurrencySafeChecker = (*WebSearchTool)(nil)

// 运行期断言：readonly 子代理按 IsConcurrencySafe(`{}`) 过滤工具集，网络工具必须放行。
func TestNetworkToolsConcurrencySafeWithEmptyParams(t *testing.T) {
	assert.True(t, (&WebFetchTool{}).IsConcurrencySafe(json.RawMessage(`{}`)))
	assert.True(t, (&WebSearchTool{}).IsConcurrencySafe(json.RawMessage(`{}`)))
}
