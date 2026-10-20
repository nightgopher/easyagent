package agent

// ────────────────────────────────────────────────────────────────────────────
//  Subagent (task tool)
//
//  task 在独立上下文里运行一个子 agent，只把它的最终回答作为工具结果交回主会话，
//  中间的工具调用和输出不进入主会话历史。子 agent 不能再派生子 agent，审批沿用
//  主会话的确认回调，主任务取消时随 ctx 一起取消。
// ────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/hwj123hwj/easyagent/sdk/ai"
	"github.com/hwj123hwj/easyagent/sdk/ai/providers"
)

// TaskToolName is the name of the subagent tool.
const TaskToolName = "task"

// DefaultTaskMaxTurns bounds a subagent run when TaskToolOptions.MaxTurns is unset.
const DefaultTaskMaxTurns = 50

const taskSystemSuffix = `

# Subagent mode
You are running as a subagent for another agent. Complete the task below on your own: you cannot ask the user questions and nobody will reply to you mid-task. Your final message is the only thing returned to the main agent, so make it a self-contained answer: state what you found or changed, with exact file paths and line numbers where relevant, and say plainly if something could not be done. Do not repeat raw tool output that the main agent does not need.`

// TaskToolOptions configures the subagent tool.
type TaskToolOptions struct {
	Model    ai.Model
	Registry *providers.Registry
	// Tools are offered to the subagent. Any tool named task is removed so
	// subagents cannot spawn further subagents.
	Tools       []Tool
	ConfirmFunc ConfirmFunc
	MaxTurns    int
}

// TaskTool runs a prompt in a fresh agent and returns its final answer.
type TaskTool struct {
	opts TaskToolOptions

	mu     sync.RWMutex
	system string
}

// NewTaskTool creates the subagent tool.
func NewTaskTool(opts TaskToolOptions) *TaskTool {
	tools := make([]Tool, 0, len(opts.Tools))
	for _, tool := range opts.Tools {
		if tool.Name() != TaskToolName {
			tools = append(tools, tool)
		}
	}
	opts.Tools = tools
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = DefaultTaskMaxTurns
	}
	return &TaskTool{opts: opts}
}

// SetSystemPrompt sets the base system prompt shared with the main agent.
// It is set after construction because the main prompt lists this tool.
func (t *TaskTool) SetSystemPrompt(system string) {
	t.mu.Lock()
	t.system = system
	t.mu.Unlock()
}

type taskParams struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	ReadOnly    bool   `json:"readonly,omitempty"`
}

func (t *TaskTool) Name() string { return TaskToolName }
func (t *TaskTool) Description() string {
	return "Delegate a self-contained task to a subagent with its own fresh context. Only the subagent's final answer comes back, so use it for broad searches or investigations whose intermediate reads would otherwise fill this conversation. The subagent cannot see this conversation: put every detail it needs into prompt. Set readonly to limit it to read and search tools; several readonly tasks run in parallel."
}
func (t *TaskTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description": map[string]any{"type": "string", "description": "A short (3-6 word) label for the task."},
			"prompt":      map[string]any{"type": "string", "description": "Complete instructions for the subagent, including goal, relevant paths and what to report back."},
			"readonly":    map[string]any{"type": "boolean", "description": "Give the subagent only read-only tools (read, grep, find, ls). Readonly tasks can run in parallel."},
		},
		"required": []string{"description", "prompt"},
	}
}

func (t *TaskTool) Validate(raw json.RawMessage) (json.RawMessage, error) {
	var params taskParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if strings.TrimSpace(params.Description) == "" {
		params.Description = "subagent task"
	}
	return json.Marshal(params)
}

// IsConcurrencySafe lets readonly subagents run in parallel batches.
func (t *TaskTool) IsConcurrencySafe(raw json.RawMessage) bool {
	var params taskParams
	return json.Unmarshal(raw, &params) == nil && params.ReadOnly
}

func (t *TaskTool) Execute(ctx context.Context, raw json.RawMessage, onUpdate func(PartialResult)) (ToolResult, error) {
	var params taskParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return ToolResult{IsError: true}, err
	}
	t.mu.RLock()
	system := t.system
	t.mu.RUnlock()

	child := New(Options{
		Model:              t.opts.Model,
		Registry:           t.opts.Registry,
		System:             system + taskSystemSuffix,
		Tools:              t.childTools(params.ReadOnly),
		MaxTurns:           t.opts.MaxTurns,
		ConfirmFunc:        t.childConfirm(params.Description),
		LoopDetectSettings: DefaultLoopDetectSettings(),
	})

	var mu sync.Mutex
	steps, usage := 0, ai.Usage{}
	unsubscribe := child.Subscribe(func(_ context.Context, event AgentEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch e := event.(type) {
		case EventToolExecutionStart:
			steps++
			if onUpdate != nil {
				onUpdate(PartialResult{Content: fmt.Sprintf("%s · step %d · %s", params.Description, steps, e.ToolName)})
			}
		case EventTurnEnd:
			if m, ok := e.Message.(ai.AssistantMessage); ok {
				usage.InputTokens += m.Usage.InputTokens
				usage.OutputTokens += m.Usage.OutputTokens
			}
		}
	})
	defer unsubscribe()

	answer, err := child.Prompt(ctx, ai.NewTextUserMessage(params.Prompt))
	mu.Lock()
	footer := fmt.Sprintf("\n\n[subagent %q: %d tool calls, %d input / %d output tokens]", params.Description, steps, usage.InputTokens, usage.OutputTokens)
	mu.Unlock()
	if err != nil {
		content := fmt.Sprintf("subagent failed: %v", err)
		if text := strings.TrimSpace(answer.Text); text != "" {
			content += "\n\nPartial answer:\n" + text
		}
		return ToolResult{IsError: true, Content: content + footer}, err
	}
	text := strings.TrimSpace(answer.Text)
	if text == "" {
		text = "(the subagent finished without a final answer)"
	}
	return ToolResult{Content: text + footer}, nil
}

// childTools 返回子 agent 可用的工具；readonly 时只保留声明为并发安全的工具
// （read / grep / find / ls 这类只读工具），不需要按名字维护白名单。
func (t *TaskTool) childTools(readOnly bool) []Tool {
	if !readOnly {
		return t.opts.Tools
	}
	var tools []Tool
	for _, tool := range t.opts.Tools {
		if checker, ok := tool.(ConcurrencySafeChecker); ok && checker.IsConcurrencySafe(json.RawMessage(`{}`)) {
			tools = append(tools, tool)
		}
	}
	return tools
}

// childConfirm 沿用主会话的审批，并在描述前标明来自哪个子任务。
func (t *TaskTool) childConfirm(label string) ConfirmFunc {
	confirm := t.opts.ConfirmFunc
	if confirm == nil {
		return nil
	}
	return func(ctx context.Context, req ConfirmationRequest) ConfirmDecision {
		req.Description = fmt.Sprintf("[subagent: %s] %s", label, req.Description)
		return confirm(ctx, req)
	}
}
