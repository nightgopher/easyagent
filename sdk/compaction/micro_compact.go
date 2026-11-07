package compaction

import (
	"github.com/nightgopher/easyagent/sdk/ai"
)

// clearedPlaceholder 替换旧 tool result 内容的占位符。
// 保留痕迹让 Agent 知道此处曾有工具输出（可重读/重查），而非凭空消失。
const clearedPlaceholder = "[older tool result cleared to save context]"

// compactableTools 可被 MicroCompact 清理的只读/输出型工具。
// 这些工具的 result 体积大、价值随时间递减（旧命令输出、旧文件内容早用完）。
// 写操作（edit/write）的 result 通常是简短确认，价值高体积小，不清理。
var compactableTools = map[string]bool{
	"search_tools": true,
	"computer":     true,
	"read":         true,
	"bash":         true,
	"grep":         true,
	"find":         true,
	"ls":           true,
	"web_fetch":    true,
}

// MicroCompact 清理历史中旧的只读工具结果，保留最近 keepRecent 个完整。
// 不调 LLM——纯本地文本替换，零成本零延迟零幻觉。
// 返回（新历史, 实际清理掉的 tool result 数）。
//
// 算法（参考 cc-haha microCompact.ts）：
//  1. 遍历历史，建立 ToolCallID → 工具名 映射（ToolResultMessage 只有 ID，
//     不带工具名，需从 AssistantMessage.ToolCalls 回溯关联）
//  2. 收集所有"可压缩工具"的 tool result 的（索引, ID）
//  3. 除最近 keepRecent 个外，把其余的 Content 替换为占位符
//
// 原地替换消息内容，不改变历史长度/结构，保持每个 turn 的完整性
// （assistant tool_call + tool result 配对不变）。
func MicroCompact(history []ai.Message, keepRecent int) ([]ai.Message, int) {
	return MicroCompactWithBudget(history, keepRecent, 0)
}

// MicroCompactWithBudget also retains recent complete tool results within a
// token budget, preferring file reads to older search output. The count is a
// minimum. A zero budget keeps the original count-only behavior for SDK callers.
func MicroCompactWithBudget(history []ai.Message, keepRecent, keepTokens int) ([]ai.Message, int) {
	if keepRecent < 0 {
		keepRecent = 0
	}

	// 1. 建 ToolCallID → 工具名 映射
	toolNameByID := make(map[string]string)
	for _, msg := range history {
		if am, ok := msg.(ai.AssistantMessage); ok {
			for _, tc := range am.ToolCalls {
				toolNameByID[tc.ID] = tc.Name
			}
		}
	}

	// 2. 收集可压缩 tool result 的（索引, ID），按出现顺序
	type resultRef struct {
		idx  int
		name string
	}
	var compactable []resultRef
	for i, msg := range history {
		if tr, ok := msg.(ai.ToolResultMessage); ok {
			if name, found := toolNameByID[tr.ToolCallID]; found && compactableTools[name] {
				compactable = append(compactable, resultRef{idx: i, name: name})
			}
		}
	}

	// 不足或刚好 keepRecent 个 → 不清理
	if len(compactable) <= keepRecent {
		return history, 0
	}

	// 3. Always keep the last keepRecent results. Within the remaining budget,
	// keep recent source reads first, including different ranges of the same
	// file, then other results. Never resurrect an earlier placeholder.
	keepFrom := len(compactable) - keepRecent
	keep := make(map[int]bool, len(compactable))
	used := 0
	for _, ref := range compactable[keepFrom:] {
		keep[ref.idx] = true
		used += EstimateTokens([]ai.Message{history[ref.idx]})
	}
	if keepTokens > 0 {
		for _, readsOnly := range []bool{true, false} {
			for i := keepFrom - 1; i >= 0; i-- {
				ref := compactable[i]
				if (ref.name == "read") != readsOnly {
					continue
				}
				tr := history[ref.idx].(ai.ToolResultMessage)
				if tr.Content == clearedPlaceholder && len(tr.Images) == 0 {
					continue
				}
				cost := EstimateTokens([]ai.Message{tr})
				if used <= keepTokens && cost <= keepTokens-used {
					keep[ref.idx] = true
					used += cost
				}
			}
		}
	}
	cleared := 0
	// 清理不在保留集合中的旧结果
	for _, ref := range compactable[:keepFrom] {
		if keep[ref.idx] {
			continue
		}
		if tr, ok := history[ref.idx].(ai.ToolResultMessage); ok {
			// 占位符可能比原内容长（原内容极短时），但通常短得多；只在确实能省时清理
			if len(clearedPlaceholder) < len(tr.Content) || len(tr.Images) > 0 {
				tr.Content = clearedPlaceholder
				tr.Images = nil
				history[ref.idx] = tr // 值类型，需重新赋值回 slice
				cleared++
			}
		}
	}

	return history, cleared
}
