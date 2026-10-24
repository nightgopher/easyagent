package server

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nightgopher/easyagent/sdk/skill"
)

type localSkillJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	// DirName is the skill's directory name, which the market uses to uninstall it.
	DirName string `json:"dir_name"`
	Root    string `json:"root"`
	// Origin says which skill root it came from: "agents", "claude" or "custom".
	Origin string `json:"origin"`
	// Managed is true when the skill market installed it, so the market can uninstall it.
	Managed bool `json:"managed"`
	// ShadowedBy is the path of an earlier skill with the same name that wins over this one.
	ShadowedBy      string `json:"shadowed_by,omitempty"`
	ModelInvocation bool   `json:"model_invocation"`
}

type localSkillRootJSON struct {
	Path   string `json:"path"`
	Origin string `json:"origin"`
}

type localSkillProblemJSON struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func skillRootOrigin(root string) string {
	slash := filepath.ToSlash(filepath.Clean(root))
	switch {
	case strings.HasSuffix(slash, "/.agents/skills"):
		return "agents"
	case strings.HasSuffix(slash, "/.claude/skills"):
		return "claude"
	default:
		return "custom"
	}
}

// listLocalSkills serves GET /skills/local: every skill found in the host's global skill
// directories, whoever put it there. The market's own "installed" list only knows skills it
// installed, so hand-written ones and ones from other tools would otherwise never show up.
func (s *Server) listLocalSkills(w http.ResponseWriter, _ *http.Request) {
	managed := map[string]bool{}
	if client := s.marketClient(); client != nil {
		if installed, err := client.ListInstalled(); err == nil {
			for _, item := range installed {
				managed[filepath.Clean(item.Dir)] = true
			}
		}
	}

	skills := []localSkillJSON{}
	roots := []localSkillRootJSON{}
	problems := []localSkillProblemJSON{}
	winner := map[string]string{}
	for _, root := range s.app.GlobalSkillDirs() {
		origin := skillRootOrigin(root)
		roots = append(roots, localSkillRootJSON{Path: root, Origin: origin})
		result := skill.LoadFromDirs(root)
		for _, d := range result.Diagnostics {
			problems = append(problems, localSkillProblemJSON{Path: d.Path, Message: d.Message})
		}
		for _, sk := range result.Skills {
			entry := localSkillJSON{
				Name:            sk.Name,
				Description:     sk.Description,
				Path:            sk.FilePath,
				DirName:         filepath.Base(sk.BaseDir),
				Root:            root,
				Origin:          origin,
				Managed:         managed[filepath.Clean(sk.BaseDir)],
				ModelInvocation: !sk.DisableModelInvocation,
			}
			if first, dup := winner[sk.Name]; dup {
				entry.ShadowedBy = first
			} else {
				winner[sk.Name] = sk.FilePath
			}
			skills = append(skills, entry)
		}
	}
	sort.SliceStable(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	writeJSON(w, map[string]any{"skills": skills, "roots": roots, "problems": problems})
}
