package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hwj123hwj/easyagent/internal/app"
	"github.com/hwj123hwj/easyagent/sdk/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type localSkillsResponse struct {
	Skills   []localSkillJSON        `json:"skills"`
	Roots    []localSkillRootJSON    `json:"roots"`
	Problems []localSkillProblemJSON `json:"problems"`
}

func writeTestSkill(t *testing.T, root, dir, name, description string) string {
	t.Helper()
	skillDir := filepath.Join(root, dir)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))
	return skillDir
}

func newSkillsServer(t *testing.T, home string, skillDirs []string) *Server {
	t.Helper()
	t.Setenv("HOME", home)
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Provider = "openai"
	cfg.OpenAIAPIKey = "test-key"
	cfg.OpenAIBaseURL = "http://localhost:4001"
	application, err := app.New(app.AppOptions{Config: cfg, SkillDirs: skillDirs})
	require.NoError(t, err)
	t.Cleanup(func() { application.Close() })
	return New(application, nil)
}

func getLocalSkills(t *testing.T, srv *Server) localSkillsResponse {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, localReq(http.MethodGet, "/skills/local", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out localSkillsResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	return out
}

func TestListLocalSkills_ShowsEverySkillWhoeverInstalledIt(t *testing.T) {
	home := t.TempDir()
	agents := filepath.Join(home, ".agents", "skills")
	claude := filepath.Join(home, ".claude", "skills")
	writeTestSkill(t, agents, "hand-written", "hand-written", "Written by hand")
	marketDir := writeTestSkill(t, agents, "from-market", "from-market", "Installed from the market")
	require.NoError(t, os.WriteFile(filepath.Join(marketDir, ".easyagent-market.json"), []byte(`{"id":7,"name":"from-market"}`), 0o644))
	writeTestSkill(t, claude, "other-tool", "other-tool", "Shared with Claude Code")

	out := getLocalSkills(t, newSkillsServer(t, home, nil))

	require.Len(t, out.Skills, 3)
	names := []string{out.Skills[0].Name, out.Skills[1].Name, out.Skills[2].Name}
	assert.Equal(t, []string{"from-market", "hand-written", "other-tool"}, names, "sorted by name")
	byName := map[string]localSkillJSON{}
	for _, sk := range out.Skills {
		byName[sk.Name] = sk
	}
	assert.True(t, byName["from-market"].Managed, "only market installs can be uninstalled from the UI")
	assert.False(t, byName["hand-written"].Managed)
	assert.False(t, byName["other-tool"].Managed)
	assert.Equal(t, "agents", byName["hand-written"].Origin)
	assert.Equal(t, "claude", byName["other-tool"].Origin)
	assert.Equal(t, "from-market", byName["from-market"].DirName)
	assert.Equal(t, "Written by hand", byName["hand-written"].Description)
	assert.True(t, byName["hand-written"].ModelInvocation)
	assert.Equal(t, filepath.Join(agents, "hand-written", "SKILL.md"), byName["hand-written"].Path)
	require.Len(t, out.Roots, 2)
	assert.Equal(t, agents, out.Roots[0].Path)
	assert.Empty(t, out.Problems)
}

func TestListLocalSkills_MarksTheShadowedDuplicate(t *testing.T) {
	home := t.TempDir()
	agents := filepath.Join(home, ".agents", "skills")
	claude := filepath.Join(home, ".claude", "skills")
	writeTestSkill(t, agents, "shared", "dup", "The one that wins")
	writeTestSkill(t, claude, "shared", "dup", "Loses to the earlier directory")

	out := getLocalSkills(t, newSkillsServer(t, home, nil))

	require.Len(t, out.Skills, 2)
	winner, loser := out.Skills[0], out.Skills[1]
	if winner.Origin != "agents" {
		winner, loser = loser, winner
	}
	assert.Empty(t, winner.ShadowedBy)
	assert.Equal(t, filepath.Join(agents, "shared", "SKILL.md"), loser.ShadowedBy)
}

func TestListLocalSkills_ReportsLoaderWarningsAlongsideTheSkill(t *testing.T) {
	home := t.TempDir()
	agents := filepath.Join(home, ".agents", "skills")
	writeTestSkill(t, agents, "fine", "fine", "Works")
	sloppy := filepath.Join(agents, "sloppy")
	require.NoError(t, os.MkdirAll(sloppy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sloppy, "SKILL.md"), []byte("---\nname: Sloppy_Name\n---\n\nbody"), 0o644))

	out := getLocalSkills(t, newSkillsServer(t, home, nil))

	// The loader only warns about naming and missing descriptions: the skill is still loaded,
	// so it must still be listed, with the warning next to it instead of silently dropped.
	require.Len(t, out.Skills, 2)
	require.NotEmpty(t, out.Problems)
	for _, p := range out.Problems {
		assert.Equal(t, filepath.Join(sloppy, "SKILL.md"), p.Path)
		assert.NotEmpty(t, p.Message)
	}
}

func TestListLocalSkills_FollowsTheConfiguredDirectories(t *testing.T) {
	home := t.TempDir()
	custom := filepath.Join(t.TempDir(), "my-skills")
	writeTestSkill(t, filepath.Join(home, ".agents", "skills"), "ignored", "ignored", "Not loaded when dirs are configured")
	writeTestSkill(t, custom, "mine", "mine", "In the configured directory")

	out := getLocalSkills(t, newSkillsServer(t, home, []string{custom}))

	require.Len(t, out.Skills, 1, "the listing must match what a session actually loads")
	assert.Equal(t, "mine", out.Skills[0].Name)
	assert.Equal(t, "custom", out.Skills[0].Origin)
}

func TestListLocalSkills_EmptyIsAnEmptyListNotNull(t *testing.T) {
	w := httptest.NewRecorder()
	srv := newSkillsServer(t, t.TempDir(), nil)
	srv.Handler().ServeHTTP(w, localReq(http.MethodGet, "/skills/local", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"skills":[]`)
	assert.Contains(t, w.Body.String(), `"problems":[]`)
}
