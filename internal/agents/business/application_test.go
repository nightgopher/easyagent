package business

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/prompt"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/nightgopher/easyagent/sdk/skill"
	"github.com/nightgopher/easyagent/sdk/tools"
	"github.com/stretchr/testify/require"
)

func TestSelectedCapabilitiesAndProgressiveSkills(t *testing.T) {
	a := Application{Definition: agentdata.Agent{ID: "a", Name: "Support", Instructions: "Own role", Tools: []string{"read"}, Skills: []string{"support"}}}
	selected := a.FilterSkills([]skill.Skill{{Name: "coding", Content: "PRIVATE"}, {Name: "support", Description: "Customer support", Content: "PROCEDURE"}})
	require.Len(t, selected, 1)
	toolList := a.BuildTools(runtime.ToolBuildOptions{Workspace: t.TempDir(), Skills: selected, ExtensionTools: []agent.Tool{tools.NewLocalTimeTool()}})
	names := []string{}
	var reader agent.Tool
	for _, tool := range toolList {
		names = append(names, tool.Name())
		if tool.Name() == "read_skill" {
			reader = tool
		}
	}
	require.Equal(t, []string{"read", "read_skill"}, names)
	text := a.BuildPrompt(runtime.PromptBuildOptions{Skills: selected, ContextFiles: []prompt.ContextFile{{Content: "PRIVATE GLOBAL MEMORY"}}}, "", "")
	require.Contains(t, text, "Customer support")
	require.NotContains(t, text, "PROCEDURE")
	require.NotContains(t, text, "PRIVATE")
	_, err := reader.Execute(context.Background(), json.RawMessage(`{"name":"coding"}`), nil)
	require.Error(t, err)
	result, err := reader.Execute(context.Background(), json.RawMessage(`{"name":"support"}`), nil)
	require.NoError(t, err)
	require.Equal(t, "PROCEDURE", result.Content)
	require.Empty(t, a.BuildTools(runtime.ToolBuildOptions{AllowedTools: []string{"local_time"}, Skills: selected}))
	require.Empty(t, a.BuildTools(runtime.ToolBuildOptions{BlockedTools: []string{"read", "read_skill"}, Skills: selected}))
}

func TestBusinessFilesStayInWorkspace(t *testing.T) {
	a := Application{Definition: agentdata.Agent{Tools: []string{"read", "write"}}}
	for _, tool := range a.BuildTools(runtime.ToolBuildOptions{Workspace: t.TempDir()}) {
		input, err := json.Marshal(map[string]string{"path": t.TempDir() + "/outside.txt", "content": "should not escape"})
		require.NoError(t, err)
		result, err := tool.Execute(context.Background(), input, nil)
		require.True(t, err != nil || result.IsError)
	}
}
