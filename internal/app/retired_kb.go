package app

import (
	"context"
	"encoding/json"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/runtime"
)

// Historical sessions remain readable without inheriting coding tools or
// reviving the removed knowledge-base application.
const retiredKBApplication = "kb"
const retiredKBNotice = "知识库 Agent 已下线，历史工具不可用；知识库文件和浏览功能仍然保留。"

var retiredKBToolNames = []string{"kb_search", "kb_read", "kb_list", "kb_save", "kb_maintain"}

type retiredKBApp struct{}

func (retiredKBApp) NewSessionExt() runtime.SessionExt { return nil }
func (retiredKBApp) BuildPrompt(runtime.PromptBuildOptions, string, string) string {
	return retiredKBNotice + "这是历史会话，只能基于已有对话回答。"
}
func (retiredKBApp) BuildTools(runtime.ToolBuildOptions) []agent.Tool {
	list := make([]agent.Tool, 0, len(retiredKBToolNames))
	for _, name := range retiredKBToolNames {
		list = append(list, retiredKBTool{name: name})
	}
	return list
}

type retiredKBTool struct{ name string }

func (t retiredKBTool) Name() string                                        { return t.name }
func (retiredKBTool) Description() string                                   { return retiredKBNotice }
func (retiredKBTool) Parameters() map[string]any                            { return map[string]any{"type": "object"} }
func (retiredKBTool) Validate(raw json.RawMessage) (json.RawMessage, error) { return raw, nil }
func (retiredKBTool) Execute(context.Context, json.RawMessage, func(agent.PartialResult)) (agent.ToolResult, error) {
	return agent.ToolResult{Content: retiredKBNotice, IsError: true}, nil
}

var _ runtime.Application = retiredKBApp{}
