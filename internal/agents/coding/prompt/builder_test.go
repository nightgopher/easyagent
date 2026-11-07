package prompt

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/stretchr/testify/require"
)

type capabilityTestTool string

func (t capabilityTestTool) Name() string                                        { return string(t) }
func (capabilityTestTool) Description() string                                   { return "test tool" }
func (capabilityTestTool) Parameters() map[string]any                            { return nil }
func (capabilityTestTool) Validate(raw json.RawMessage) (json.RawMessage, error) { return raw, nil }
func (capabilityTestTool) Execute(context.Context, json.RawMessage, func(agent.PartialResult)) (agent.ToolResult, error) {
	return agent.ToolResult{}, nil
}

func TestPromptReflectsInitialCapabilitiesAndEncouragesProgress(t *testing.T) {
	withoutBash := BuildSystemPrompt(Options{Tools: []agent.Tool{capabilityTestTool("read"), capabilityTestTool("edit"), capabilityTestTool("search_tools")}})
	require.Contains(t, withoutBash, "bash tool is not in the initial tool list")
	require.Contains(t, withoutBash, "available or discoverable tools")
	require.Contains(t, withoutBash, "move to a small verifiable change")
	require.NotContains(t, withoutBash, "you cannot run commands")

	withBash := BuildSystemPrompt(Options{Tools: []agent.Tool{capabilityTestTool("bash"), capabilityTestTool("read")}})
	require.NotContains(t, withBash, "bash tool is not in the initial tool list")
	require.Contains(t, withBash, "Avoid destructive commands")
}
