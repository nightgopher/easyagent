package compaction

import (
	"strings"

	"github.com/nightgopher/easyagent/sdk/ai"
)

// EstimateTokens 估算消息列表的 token 数。
// 不要求精确（不需要完整 tokenizer），按 4 字符 ≈ 1 token 估算。
// 用于触发 ShouldCompact 前的快速判断。
func EstimateTokens(msgs []ai.Message) int {
	return estimateTokens(msgs, "")
}

// EstimateTokensForModel estimates the messages actually serialized by the
// selected provider. In particular, OpenAI-compatible requests do not send
// stored reasoning, whereas Anthropic and DeepV send thinking blocks.
func EstimateTokensForModel(msgs []ai.Message, model ai.Model) int {
	return estimateTokens(msgs, model.Provider)
}

func estimateTokens(msgs []ai.Message, provider string) int {
	total := 0
	for _, msg := range msgs {
		total += estimateMessageTokensForProvider(msg, provider)
	}
	return total
}

func estimateMessageTokensForProvider(msg ai.Message, provider string) int {
	var text string
	imageTokens := 0
	switch m := msg.(type) {
	case ai.UserMessage:
		for _, block := range m.Content {
			if block.Type == "image" {
				imageTokens += 4096
			}
			if block.Type == "text" {
				text += block.Text
			}
		}
	case ai.AssistantMessage:
		text = m.Text
		switch provider {
		case "openai":
			// buildOpenAIRequest serializes only Text and ToolCalls.
		case "anthropic":
			for _, block := range m.ThinkingBlocks {
				text += block.Thinking + block.Signature + block.Data
			}
		case "deepv":
			// DeepV drops assistant messages with no text or tool calls.
			if m.Text != "" || len(m.ToolCalls) > 0 {
				if len(m.ThinkingBlocks) == 0 {
					text += m.Thinking
				} else {
					for _, block := range m.ThinkingBlocks {
						if block.Type == "thinking" {
							text += block.Thinking
						}
					}
				}
			}
		default:
			// Preserve the conservative legacy estimate for custom providers.
			text += m.Thinking
		}
		for _, tc := range m.ToolCalls {
			text += tc.Name + tc.Args
		}
	case ai.ToolResultMessage:
		text = m.Content
		imageTokens = len(m.Images) * 4096
	}
	return max(1, len(text)/4) + imageTokens
}

// EstimateTextTokens 估算纯文本的 token 数。
func EstimateTextTokens(text string) int {
	return max(1, len(text)/4)
}

// SplitMessages 将消息列表按 token 预算分割为历史部分和保留部分。
// 从后往前累积 token，直到达到 keepRecentTokens。
// SplitMessages 把消息切成 (history, recent) 两段：recent 保留最近 keepRecentTokens
// token 的消息，history 是更早的可被压缩部分。
//
// 切割点必须在"完整 turn 边界"上——即 history 的最后一条是一个交互单元的自然收尾，
// 不会把 assistant(tool_use) 和它后续的 tool_result 拆开。安全边界包括：
//   - tool 结果之后（一个工具调用 turn 完整结束）
//   - 纯文本 assistant 回复之后（一段对话完整结束）
//
// 注意：assistant 带 tool_call 时不算安全边界（它的 tool_result 还在后面）。
func SplitMessages(msgs []ai.Message, keepRecentTokens int) (history []ai.Message, recent []ai.Message) {
	return splitMessages(msgs, keepRecentTokens, "")
}

// SplitMessagesForModel uses the same provider-aware estimate as the request
// budget, while preserving complete assistant/tool-result boundaries.
func SplitMessagesForModel(msgs []ai.Message, keepRecentTokens int, model ai.Model) (history []ai.Message, recent []ai.Message) {
	return splitMessages(msgs, keepRecentTokens, model.Provider)
}

func splitMessages(msgs []ai.Message, keepRecentTokens int, provider string) (history []ai.Message, recent []ai.Message) {
	if len(msgs) == 0 {
		return nil, nil
	}

	// 从后往前累积 token
	recentTokens := 0
	cutIndex := len(msgs)

	for i := len(msgs) - 1; i >= 0; i-- {
		msgTokens := estimateMessageTokensForProvider(msgs[i], provider)
		if recentTokens+msgTokens > keepRecentTokens {
			break
		}
		recentTokens += msgTokens
		cutIndex = i
	}

	// 往前找到最近的安全切割点（msgs[cutIndex-1] 是完整 turn 收尾）。
	safe := safeCutBoundaries(msgs)
	for cutIndex > 0 && !safe[cutIndex] {
		cutIndex--
	}

	if cutIndex == 0 {
		return nil, msgs
	}

	return msgs[:cutIndex], msgs[cutIndex:]
}

// A tool batch is complete only after every call has a result. Cutting after
// its first result can otherwise leave orphan results in the retained context.
func safeCutBoundaries(msgs []ai.Message) []bool {
	safe := make([]bool, len(msgs)+1)
	pending := make(map[string]bool)
	for i, msg := range msgs {
		switch m := msg.(type) {
		case ai.AssistantMessage:
			for _, call := range m.ToolCalls {
				pending[call.ID] = true
			}
			safe[i+1] = len(pending) == 0
		case ai.ToolResultMessage:
			delete(pending, m.ToolCallID)
			safe[i+1] = len(pending) == 0
		}
	}
	return safe
}

// SummarizePrompt 生成摘要的 prompt。
// customInstructions: 可选的用户自定义摘要指令，会追加到基础 prompt 中。
func SummarizePrompt(history []ai.Message, customInstructions string) string {
	var b strings.Builder
	b.WriteString("Summarize the following conversation history. ")
	b.WriteString("Focus on:\n")
	b.WriteString("1. Goal: Preserve the user's current goal, explicit acceptance criteria, constraints, and later corrections.\n")
	b.WriteString("2. Progress: Separate actual changes and verified results from plans, unfinished work, and unverified checks.\n")
	b.WriteString("3. Key Decisions: Preserve important choices and unresolved questions.\n")
	b.WriteString("4. Next Steps: State the next concrete action and any blocker.\n")
	b.WriteString("5. Critical Context: Preserve relevant file paths and available function signatures. Do not invent missing source details; identify what must be reread.\n")
	if customInstructions != "" {
		b.WriteString("\nAdditional instructions from user:\n")
		b.WriteString(customInstructions)
		b.WriteString("\n")
	}
	b.WriteString("\nConversation history:\n\n")

	for _, msg := range history {
		switch m := msg.(type) {
		case ai.UserMessage:
			for _, block := range m.Content {
				if block.Type == "text" {
					b.WriteString("User: ")
					b.WriteString(block.Text)
					b.WriteString("\n")
				}
			}
		case ai.AssistantMessage:
			b.WriteString("Assistant: ")
			b.WriteString(m.Text)
			b.WriteString("\n")
			for _, tc := range m.ToolCalls {
				b.WriteString("  Tool call: ")
				b.WriteString(tc.Name)
				b.WriteString("(")
				b.WriteString(tc.Args)
				b.WriteString(")\n")
			}
		case ai.ToolResultMessage:
			b.WriteString("Tool result: ")
			// 截断过长的 tool result
			content := m.Content
			if runes := []rune(content); len(runes) > 500 {
				content = string(runes[:500]) + "...(truncated)"
			}
			b.WriteString(content)
			b.WriteString("\n")
		}
	}

	return b.String()
}
