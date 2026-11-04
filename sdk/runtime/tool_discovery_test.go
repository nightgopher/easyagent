package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/extensions"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
	"github.com/nightgopher/easyagent/sdk/skill"
	"github.com/nightgopher/easyagent/sdk/tools"
	"github.com/stretchr/testify/require"
)

type discoveryApplication struct{}

func (*discoveryApplication) BuildTools(opts ToolBuildOptions) []agent.Tool {
	if !toolAllowed("web_fetch", opts.AllowedTools, opts.BlockedTools) {
		return nil
	}
	return []agent.Tool{tools.NewWebFetchTool()}
}
func (*discoveryApplication) BuildPrompt(o PromptBuildOptions, _, _ string) string {
	var b strings.Builder
	for _, tool := range o.Tools {
		data, _ := json.Marshal(tool.Parameters())
		b.WriteString(tool.Name() + " " + string(data))
	}
	return b.String() + skill.FormatForSystemPrompt(o.Skills)
}
func (*discoveryApplication) NewSessionExt() SessionExt { return nil }
func (*discoveryApplication) SubagentsEnabled() bool    { return true }

func TestToolDiscoveryCannotBypassToolFilters(t *testing.T) {
	for _, blocked := range []string{agent.SearchToolsName, "web_fetch"} {
		t.Run(blocked, func(t *testing.T) {
			cfg := config.Default()
			cfg.DataDir, cfg.Workspace = t.TempDir(), t.TempDir()
			cfg.Provider, cfg.ToolSearch, cfg.SkillMarketEnabled = "openai", "local", false
			cfg.BlockedTools = []string{blocked}
			s, err := NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg, SkillDirs: []string{t.TempDir()}}, Dependencies{SessionMgr: sessionmgr.NewManager(cfg.DataDir), Registry: providers.NewRegistry(), ExtRegistry: extensions.NewRegistry(), Application: &discoveryApplication{}})
			require.NoError(t, err)
			defer func() { require.NoError(t, s.Close()) }()
			snapshot, err := s.ContextSnapshot(context.Background())
			require.NoError(t, err)
			for _, tool := range snapshot.Tools {
				require.NotEqual(t, blocked, tool.Name)
			}
			if blocked == agent.SearchToolsName {
				require.Equal(t, agent.ToolSearchOff, snapshot.Usage.ToolDiscovery.Mode)
				require.Equal(t, "search_tools_filtered", snapshot.Usage.ToolDiscovery.FallbackReason)
				require.Contains(t, snapshot.System, "web_fetch")
			} else {
				require.NotContains(t, s.ToolNames(), "web_fetch")
				require.Equal(t, 0, snapshot.Usage.ToolDiscovery.Searchable)
				require.NotContains(t, snapshot.System, "## Additional tools")
			}
		})
	}
}
