// Package business runs saved agent definitions on the shared session runtime.
package business

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/nightgopher/easyagent/sdk/skill"
	"github.com/nightgopher/easyagent/sdk/tools"
)

type Application struct {
	Definition agentdata.Agent
	Config     config.Config
	Store      *agentdata.Store
}

func (a Application) NewSessionExt() runtime.SessionExt { return nil }

// Skills are explicit capabilities; host-global skills are not inherited.
func (a Application) FilterSkills(all []skill.Skill) []skill.Skill {
	selected := []skill.Skill{}
	for _, s := range all {
		if slices.Contains(a.Definition.Skills, s.Name) {
			selected = append(selected, s)
		}
	}
	return selected
}

func (a Application) BuildTools(o runtime.ToolBuildOptions) []agent.Tool {
	list := []agent.Tool{
		tools.NewReadTool(tools.WithReadWorkspace(o.Workspace), tools.WithReadMaxOutputLen(o.MaxOutputLen), tools.WithReadOperations(o.FileOps)),
		tools.NewWriteTool(tools.WithWriteWorkspace(o.Workspace), tools.WithWriteOperations(o.FileOps)),
		tools.NewAskUserTool(), tools.NewLocalTimeTool(),
	}
	if a.Config.EnableWeb {
		list = append(list, tools.NewWebFetchTool(tools.WithWebFetchMaxOutputLen(o.MaxOutputLen)))
	}
	if a.Config.EnableWebSearch {
		list = append(list, tools.NewWebSearchTool())
	}
	if len(o.Skills) > 0 {
		list = append(list, skillTool{skills: o.Skills})
	}
	list = append(list, o.ExtensionTools...)
	filtered := []agent.Tool{}
	for _, t := range list {
		if (slices.Contains(a.Definition.Tools, t.Name()) || t.Name() == "read_skill" && len(o.Skills) > 0) && (len(o.AllowedTools) == 0 || slices.Contains(o.AllowedTools, t.Name())) && !slices.Contains(o.BlockedTools, t.Name()) {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

func (a Application) BuildPrompt(o runtime.PromptBuildOptions, _, _ string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, a persistent business assistant.\nAgent ID: %s\n\n%s\n\n", a.Definition.Name, a.Definition.ID, a.Definition.Instructions)
	fmt.Fprintf(&b, "Your working directory is %s. Read and write only within this directory. Use the tools actually available to you; a skill does not grant additional permissions. Ask the user when essential information is missing.\n", o.CWD)
	// Business agents deliberately do not inherit global AGENTS.md memories or
	// coding project instructions. Their role comes from their saved definition.
	if len(o.Skills) > 0 {
		b.WriteString("\nEnabled skills (read_skill retrieves their instructions):\n")
	}
	for _, s := range o.Skills {
		if !s.DisableModelInvocation {
			fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
		}
	}
	return b.String()
}

var _ runtime.Application = Application{}
