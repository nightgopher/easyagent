package mcp

import (
	"testing"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/stretchr/testify/require"
)

func TestMCPDiscoveryMetadataKeepsRawNameAndDoesNotTrustAlwaysLoad(t *testing.T) {
	manager := &Manager{servers: map[string]*managedServer{"oa": {entry: ConfigEntry{Config: MCPServerConfig{AlwaysLoad: true}}}}}
	for _, always := range []bool{false, true} {
		tool := newToolAdapter(manager, "oa", &protocol.Tool{Name: "list.pending", Description: "审批", InputSchema: map[string]any{"type": "object"}}, MCPServerConfig{AlwaysLoad: always})
		info := tool.DiscoveryInfo()
		require.Equal(t, !always, info.Deferred)
		require.Equal(t, "mcp:oa", info.SourceID)
		require.Contains(t, info.Aliases, "list.pending")
		require.True(t, tool.RequiresConfirmationAvailable())
		_, summary, err := agent.DiscoveryPromptTools([]agent.Tool{tool}, agent.ToolSearchLocal)
		require.NoError(t, err)
		if always {
			require.Empty(t, summary)
		} else {
			require.Contains(t, summary, "审批")
		}
	}
}
