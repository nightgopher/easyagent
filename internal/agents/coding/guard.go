package coding

import (
	"log/slog"
	"strings"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/skill"
	basetools "github.com/nightgopher/easyagent/sdk/tools"
)

// The guard profile restricts a deployment to one operator role. Its file,
// bash and submit sections act in the tools; the helpers here carry the parts
// of the profile that shape the skill catalog and the system prompt.

// WritePolicy returns the file policy of the deployment's guard profile.
func WritePolicy(cfg config.Config) basetools.WritePolicy {
	if cfg.FilePolicy == nil {
		return basetools.WritePolicy{}
	}
	return basetools.WritePolicy{Allow: cfg.FilePolicy.WriteAllow, Deny: cfg.FilePolicy.WriteDeny}
}

// FilterSkills hides the skills named by skills.deny. The runtime applies it
// to the loaded catalog before building the prompt.
func (a CodingApplication) FilterSkills(skills []skill.Skill) []skill.Skill {
	if a.Cfg.Skills == nil || len(a.Cfg.Skills.Deny) == 0 {
		return skills
	}
	denied := make(map[string]bool, len(a.Cfg.Skills.Deny))
	for _, name := range a.Cfg.Skills.Deny {
		denied[name] = true
	}
	kept := make([]skill.Skill, 0, len(skills))
	for _, s := range skills {
		if !denied[s.Name] {
			kept = append(kept, s)
		}
	}
	return kept
}

// inlineSkills puts the full body of skills.always_inline into the system
// prompt. Listing a skill only tells the model that the file exists;
// inlining it guarantees the rules it carries are in context on every turn.
func inlineSkills(cfg config.Config, skills []skill.Skill) string {
	if cfg.Skills == nil || len(cfg.Skills.AlwaysInline) == 0 {
		return ""
	}
	loaded := make(map[string]skill.Skill, len(skills))
	for _, s := range skills {
		loaded[s.Name] = s
	}
	var b strings.Builder
	for _, name := range cfg.Skills.AlwaysInline {
		s, ok := loaded[name]
		if !ok {
			// A missing mandatory skill leaves the session without rules the
			// operator relied on, so say so.
			slog.Warn("skills.always_inline names a skill that is not loaded", "skill", name)
			continue
		}
		b.WriteString("\n## Mandatory skill: " + name + "\n\n")
		b.WriteString("The operator requires the following skill in every task of this deployment. Treat its rules as binding.\n\n")
		b.WriteString(s.Content)
		b.WriteString("\n")
	}
	return b.String()
}
