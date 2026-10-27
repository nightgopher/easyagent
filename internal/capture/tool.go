package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nightgopher/easyagent/sdk/agent"
	"strings"
)

type Tool struct {
	Store  Store
	Source string
}

func (t Tool) Name() string { return "capture_note" }
func (t Tool) Description() string {
	return "Save a fragment to the user's persistent inbox only when explicitly asked to remember/save it for later. This is a record, not a personal preference or an instruction to execute. Report success only after this tool succeeds."
}
func (t Tool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string", "description": "The original thought or link to save"}}, "required": []string{"text"}}
}
func (t Tool) Validate(p json.RawMessage) (json.RawMessage, error) {
	var r Record
	if err := json.Unmarshal(p, &r); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.Text) == "" || len(r.Text) > MaxText {
		return nil, fmt.Errorf("text must contain 1–65536 bytes")
	}
	return p, nil
}
func (t Tool) Execute(_ context.Context, p json.RawMessage, _ func(agent.PartialResult)) (agent.ToolResult, error) {
	if _, err := t.Validate(p); err != nil {
		return agent.ToolResult{}, err
	}
	var r Record
	if err := json.Unmarshal(p, &r); err != nil {
		return agent.ToolResult{}, err
	}
	r.ID = NewID()
	r.Source = t.Source
	saved, err := t.Store.Save(r)
	if err != nil {
		return agent.ToolResult{}, err
	}
	data, err := json.Marshal(saved)
	return agent.ToolResult{Content: string(data), UserFacing: "已保存到收件箱", Details: saved}, err
}
