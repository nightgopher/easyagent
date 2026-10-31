package business

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/skill"
)

// skillTool reads only explicitly selected skills, without exposing the host's
// skill directories through the workspace read tool.
type skillTool struct{ skills []skill.Skill }

func (skillTool) Name() string        { return "read_skill" }
func (skillTool) Description() string { return "Read the instructions of an enabled skill by name." }
func (skillTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}}
}
func (t skillTool) Validate(p json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(p, &input); err != nil {
		return nil, err
	}
	if skill.FindByName(t.skills, input.Name) == nil {
		return nil, fmt.Errorf("skill is not enabled for this agent")
	}
	return p, nil
}
func (t skillTool) Execute(_ context.Context, p json.RawMessage, _ func(agent.PartialResult)) (agent.ToolResult, error) {
	if _, err := t.Validate(p); err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	var input struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(p, &input)
	return agent.ToolResult{Content: skill.FindByName(t.skills, input.Name).Content}, nil
}
