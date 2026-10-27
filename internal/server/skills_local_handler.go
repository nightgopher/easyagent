package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/skill"
)

type localSkillJSON struct {
	Enabled     bool   `json:"enabled"`
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
	disabled, err := config.DisabledSkillPaths(s.app.Config().DataDir)
	if err != nil {
		writeError(w, 500, "读取技能开关失败："+err.Error())
		return
	}
	managed := map[string]bool{}
	installedError := ""
	if client := s.marketClient(); client != nil {
		if installed, err := client.ListInstalled(); err != nil {
			// Without the market's records no skill can be told apart as market-installed, so say so
			// instead of quietly dropping the badge and the uninstall button.
			installedError = err.Error()
		} else {
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
				Enabled:         !disabled[filepath.Clean(sk.FilePath)],
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
	out := map[string]any{"skills": skills, "roots": roots, "problems": problems}
	if installedError != "" {
		out["installed_error"] = installedError
	}
	writeJSON(w, out)
}

// setLocalSkillEnabled changes only the host loading preference, never the shared skill file.
func (s *Server) setLocalSkillEnabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body); err != nil || body.Enabled == nil || body.Path == "" {
		writeError(w, 400, "path 和 enabled 必须填写")
		return
	}
	found := false
	for _, root := range s.app.GlobalSkillDirs() {
		for _, loaded := range skill.LoadFromDirs(root).Skills {
			if filepath.Clean(loaded.FilePath) == filepath.Clean(body.Path) {
				found = true
				break
			}
		}
	}
	if !found {
		writeError(w, 404, "未找到当前主机的全局技能")
		return
	}
	if err := config.SetSkillEnabled(s.app.Config().DataDir, body.Path, *body.Enabled); err != nil {
		writeError(w, 500, "保存技能开关失败："+err.Error())
		return
	}
	s.listLocalSkills(w, r)
}
