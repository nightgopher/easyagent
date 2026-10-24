package coding

import (
	codingcommands "github.com/nightgopher/easyagent/internal/agents/coding/commands"
	codingtools "github.com/nightgopher/easyagent/internal/agents/coding/tools"
	"github.com/nightgopher/easyagent/sdk/slashcmd"
)

func BaseToolNames(enableBash bool) []string {
	return codingtools.BaseToolNames(enableBash)
}

func RegisterCommands(registry *slashcmd.Registry) {
	codingcommands.RegisterBuiltins(registry)
	codingcommands.RegisterWikiCommands(registry)
}
