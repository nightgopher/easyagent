package business

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/sdk/agent"
)

type memoryTool struct {
	store                                 *agentdata.Store
	agentID, workspace, sessionID, action string
}

func (t memoryTool) Name() string { return "memory_" + t.action }
func (t memoryTool) Description() string {
	switch t.action {
	case "search":
		return "Recall this agent's saved facts and preferences relevant to a query. Only current agent and current workspace memories are visible; expired entries are excluded. Check current memory rather than treating old conversation excerpts as current facts."
	case "save":
		return "Save a fact the user explicitly asked to remember. Do not automatically extract tool outputs, instructions or guesses. Use a stable key, scope agent or workspace, and optionally expires_at (RFC3339). To correct a fact, first search it and supply its id and version."
	default:
		return "Forget a memory the user asked to remove. First search it and supply its id and version."
	}
}
func (t memoryTool) Parameters() map[string]any {
	props := map[string]any{}
	var required []string
	field := func(name, kind string) { props[name] = map[string]any{"type": kind} }
	switch t.action {
	case "search":
		field("query", "string")
		required = []string{"query"}
	case "save":
		field("id", "string")
		field("version", "integer")
		field("key", "string")
		field("content", "string")
		field("expires_at", "string")
		props["scope"] = map[string]any{"type": "string", "enum": []string{"agent", "workspace"}}
		required = []string{"key", "content"}
	default:
		field("id", "string")
		field("version", "integer")
		required = []string{"id", "version"}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func (t memoryTool) Validate(p json.RawMessage) (json.RawMessage, error) {
	var input struct {
		agentdata.Memory
		Query string `json:"query"`
	}
	if err := json.Unmarshal(p, &input); err != nil {
		return nil, err
	}
	if t.action == "save" && (input.Key == "" || input.Content == "") {
		return nil, fmt.Errorf("key and content are required")
	}
	if t.action == "forget" && (input.ID == "" || input.Version < 1) {
		return nil, fmt.Errorf("id and version are required")
	}
	return p, nil
}
func (t memoryTool) RequiresConfirmation(json.RawMessage) (string, bool) {
	return "删除这条 Agent 记忆？", t.action == "forget"
}
func (t memoryTool) RequiresConfirmationAvailable() bool { return t.action == "forget" }
func (t memoryTool) Execute(ctx context.Context, p json.RawMessage, _ func(agent.PartialResult)) (agent.ToolResult, error) {
	if _, err := t.Validate(p); err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	var input struct {
		agentdata.Memory
		Query string `json:"query"`
	}
	_ = json.Unmarshal(p, &input)
	a, err := t.store.GetAgent(ctx, t.agentID)
	if err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	if a.Disabled || !a.MemoryEnabled {
		return agent.ToolResult{IsError: true}, fmt.Errorf("agent memory is not enabled")
	}
	var result any
	switch t.action {
	case "search":
		var memories []agentdata.Memory
		memories, err = t.store.SearchMemories(ctx, t.agentID, t.workspace, input.Query)
		for i := range memories {
			text := []rune(memories[i].Content)
			if len(text) > 1200 {
				memories[i].Content = string(text[:1200]) + " [truncated]"
			}
		}
		result = memories
	case "save":
		result, err = t.store.SaveMemory(ctx, t.agentID, t.workspace, "agent", t.sessionID, input.Memory)
	case "forget":
		var m agentdata.Memory
		m, err = t.store.GetMemory(ctx, t.agentID, input.ID)
		if err == nil && m.Scope == "workspace" && m.Workspace != t.workspace {
			err = agentdata.ErrNotFound
		}
		if err == nil {
			err = t.store.DeleteMemory(ctx, t.agentID, input.ID, input.Version)
		}
		result = map[string]bool{"deleted": err == nil}
	}
	if err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	data, err := json.Marshal(result)
	return agent.ToolResult{Content: string(data)}, err
}
