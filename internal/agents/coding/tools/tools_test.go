package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/operations"
	"github.com/stretchr/testify/assert"
)

type fakeTool struct{ name string }

func (f fakeTool) Name() string               { return f.name }
func (f fakeTool) Description() string        { return "" }
func (f fakeTool) Parameters() map[string]any { return nil }
func (f fakeTool) Validate(args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}
func (f fakeTool) Execute(ctx context.Context, input json.RawMessage, onUpdate func(agent.PartialResult)) (agent.ToolResult, error) {
	return agent.ToolResult{}, nil
}

func TestBuildList_FiltersAndExtensions(t *testing.T) {
	list := BuildList(ListOptions{
		Workspace:      "/tmp/project",
		MaxOutputLen:   1024,
		EnableBash:     true,
		BashOps:        operations.NewLocalOperations().Bash,
		FileOps:        operations.NewLocalOperations().Files,
		ExtensionTools: []agent.Tool{fakeTool{name: "custom"}},
		AllowedTools:   []string{"bash", "read", "custom"},
		BlockedTools:   []string{"read"},
	})

	var names []string
	for _, tool := range list {
		names = append(names, tool.Name())
	}

	assert.Equal(t, []string{"bash", "custom"}, names)
}
