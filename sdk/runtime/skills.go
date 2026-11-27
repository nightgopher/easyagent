package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/skill"
	"github.com/nightgopher/easyagent/sdk/util"
	"gopkg.in/yaml.v3"
)

// PersonalSkillDirs are the per-user skill roots shared by every project, in precedence order.
func PersonalSkillDirs() []string {
	return []string{filepath.Join(util.HomeDir(), ".agents", "skills"), filepath.Join(util.HomeDir(), ".claude", "skills")}
}

// ProjectSkillDirs are the skill roots inside a project, in precedence order.
func ProjectSkillDirs(workspace string) []string {
	return []string{filepath.Join(workspace, ".agents", "skills"), filepath.Join(workspace, ".claude", "skills")}
}

// DefaultSkillDirs includes the shared personal source used by the gateway.
// Project definitions take precedence over personal definitions with the same name.
func DefaultSkillDirs(workspace string) []string {
	return append(ProjectSkillDirs(workspace), PersonalSkillDirs()...)
}

// GlobalSkillDirs returns explicit roots exclusively, or the personal defaults
// plus the skill market's directory when no roots were explicitly configured.
// It mirrors how a session assembles its skill directories so a listing never disagrees with
// what is actually loaded.
func GlobalSkillDirs(explicit []string, marketDir string) []string {
	if len(explicit) > 0 {
		return append([]string(nil), explicit...)
	}
	dirs := PersonalSkillDirs()
	if marketDir != "" {
		dirs = append(dirs, marketDir)
	}
	return dirs
}

func sessionSkillDirs(cfg config.Config, explicit []string, workspace string) []string {
	// A guard profile's roots cannot be broadened by an entrypoint's defaults.
	if cfg.Skills != nil && len(cfg.Skills.Dirs) > 0 {
		return append([]string(nil), cfg.Skills.Dirs...)
	}
	if len(explicit) > 0 {
		return append([]string(nil), explicit...)
	}
	dirs := DefaultSkillDirs(workspace)
	if cfg.SkillMarketEnabled && cfg.SkillMarketSkillDir != "" {
		dirs = append(dirs, cfg.SkillMarketSkillDir)
	}
	return dirs
}

func skillDenied(cfg *config.SkillsConfig, name string) bool {
	return cfg != nil && slices.Contains(cfg.Deny, name)
}

// Required skills are validated after preferences and application filtering, but
// before tools or prompts are built. Optional skills retain lenient loading.
func validateRequiredSkills(cfg *config.SkillsConfig, skills []skill.Skill, diagnostics []skill.Diagnostic) error {
	if cfg == nil {
		return nil
	}
	for _, name := range cfg.AlwaysInline {
		if skillDenied(cfg, name) {
			return fmt.Errorf("required skill %q is denied", name)
		}
		index := slices.IndexFunc(skills, func(s skill.Skill) bool { return s.Name == name })
		if index < 0 {
			return fmt.Errorf("required skill %q is missing, disabled or filtered", name)
		}
		loaded := skills[index]
		if loaded.DisableModelInvocation {
			return fmt.Errorf("required skill %q disables model invocation", name)
		}
		for _, diagnostic := range diagnostics {
			if diagnostic.Path == loaded.FilePath {
				return fmt.Errorf("required skill %q is malformed: %s", name, diagnostic.Message)
			}
		}
		if err := validateRequiredSkillFile(loaded.FilePath); err != nil {
			return fmt.Errorf("required skill %q is malformed: %w", name, err)
		}
	}
	return nil
}

func validateRequiredSkillFile(path string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- selected skill root supplied by the operator.
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return fmt.Errorf("missing YAML frontmatter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return fmt.Errorf("unterminated YAML frontmatter")
	}
	// Unlike optional legacy skills, required skills must not use the loader's
	// invalid-YAML fallback or treat empty frontmatter/body as instructions.
	var metadata map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &metadata); err != nil {
		return err
	}
	for _, key := range []string{"name", "description"} {
		if value, ok := metadata[key]; ok {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s must be a string", key)
			}
		}
	}
	if value, ok := metadata["disable-model-invocation"]; ok {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("disable-model-invocation must be a boolean")
		}
	}
	if strings.TrimSpace(strings.Join(lines[end+1:], "\n")) == "" {
		return fmt.Errorf("empty instruction body")
	}
	return nil
}

// LoadedSkills is the catalog used to build this session's current system prompt.
func (s *AgentSession) LoadedSkills() []skill.Skill {
	s.skillsMu.RLock()
	defer s.skillsMu.RUnlock()
	return append([]skill.Skill(nil), s.loadedSkills...)
}
