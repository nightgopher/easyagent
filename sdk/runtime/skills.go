package runtime

import (
	"github.com/nightgopher/easyagent/sdk/skill"
	"github.com/nightgopher/easyagent/sdk/util"
	"path/filepath"
)

// PersonalSkillDirs are the per-user skill roots shared by every project, in precedence order.
func PersonalSkillDirs() []string {
	return []string{filepath.Join(util.HomeDir(), ".agents", "skills"), filepath.Join(util.HomeDir(), ".claude", "skills")}
}

// DefaultSkillDirs includes the shared personal source used by the gateway.
// Project definitions take precedence over personal definitions with the same name.
func DefaultSkillDirs(workspace string) []string {
	return append([]string{filepath.Join(workspace, ".agents", "skills"), filepath.Join(workspace, ".claude", "skills")}, PersonalSkillDirs()...)
}

// GlobalSkillDirs are the project-independent skill directories a session loads from: the
// explicitly configured ones (or the personal defaults), plus the skill market's directory.
// It mirrors how a session assembles its skill directories so a listing never disagrees with
// what is actually loaded.
func GlobalSkillDirs(explicit []string, marketDir string) []string {
	dirs := append([]string(nil), explicit...)
	if len(dirs) == 0 {
		dirs = PersonalSkillDirs()
	}
	if marketDir != "" {
		dirs = append(dirs, marketDir)
	}
	return dirs
}

// LoadedSkills is the catalog used to build this session's current system prompt.
func (s *AgentSession) LoadedSkills() []skill.Skill {
	s.skillsMu.RLock()
	defer s.skillsMu.RUnlock()
	return append([]skill.Skill(nil), s.loadedSkills...)
}
