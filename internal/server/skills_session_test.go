package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capabilitySkill struct {
	Name            string `json:"name"`
	Source          string `json:"source"`
	Scope           string `json:"scope"`
	Origin          string `json:"origin"`
	Overrides       string `json:"overrides"`
	ModelInvocation bool   `json:"model_invocation"`
}

func createSkillsSession(t *testing.T, srv *Server, cwd string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"cwd": cwd})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, localReq(http.MethodPost, "/sessions", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var created SessionResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&created))
	return created.ID
}

func sessionSkills(t *testing.T, srv *Server, id string) map[string]capabilitySkill {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, localReq(http.MethodGet, "/sessions/"+id+"/capabilities", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out struct {
		Skills []capabilitySkill `json:"skills"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	byName := map[string]capabilitySkill{}
	for _, sk := range out.Skills {
		_, dup := byName[sk.Name]
		require.False(t, dup, "a session loads each skill name once: %s", sk.Name)
		byName[sk.Name] = sk
	}
	return byName
}

func TestCapabilities_SeparatesProjectFromPersonalSkillsAndFlagsOverrides(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	personal := filepath.Join(home, ".agents", "skills")
	writeTestSkill(t, personal, "shared", "shared", "Personal copy")
	writeTestSkill(t, personal, "personal-only", "personal-only", "Only in the home directory")
	writeTestSkill(t, filepath.Join(workspace, ".agents", "skills"), "shared", "shared", "Project copy wins")
	writeTestSkill(t, filepath.Join(workspace, ".claude", "skills"), "from-claude-dir", "from-claude-dir", "Compatible project directory")
	srv := newSkillsServer(t, home, nil)

	skills := sessionSkills(t, srv, createSkillsSession(t, srv, workspace))

	shared := skills["shared"]
	assert.Equal(t, "project", shared.Scope, "the project's copy is the one loaded")
	assert.Equal(t, "agents", shared.Origin)
	assert.Equal(t, filepath.Join(personal, "shared", "SKILL.md"), shared.Overrides, "tells which global skill it hides")
	assert.True(t, shared.ModelInvocation)

	claude := skills["from-claude-dir"]
	assert.Equal(t, "project", claude.Scope)
	assert.Equal(t, "claude", claude.Origin)
	assert.Empty(t, claude.Overrides)

	only := skills["personal-only"]
	assert.Equal(t, "personal", only.Scope)
	assert.Empty(t, only.Origin)
	assert.Empty(t, only.Overrides)
}

func TestCapabilities_PersonalSkillsStayPersonalWhenTheWorkspaceIsTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	writeTestSkill(t, filepath.Join(home, ".agents", "skills"), "mine", "mine", "Lives under the home directory")
	srv := newSkillsServer(t, home, nil)

	// The workspace contains ~/.agents/skills, but those are still the user's own skills, not the project's.
	skills := sessionSkills(t, srv, createSkillsSession(t, srv, home))

	assert.Equal(t, "personal", skills["mine"].Scope)
}

func TestCapabilities_PersonalSkillsStayPersonalWhenTheWorkspaceContainsTheHomeDirectory(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "home")
	writeTestSkill(t, filepath.Join(home, ".agents", "skills"), "mine", "mine", "Inside a workspace that contains the home directory")
	srv := newSkillsServer(t, home, nil)

	// The skill file lies under the workspace, but not in the workspace's own skill directories.
	skills := sessionSkills(t, srv, createSkillsSession(t, srv, parent))

	assert.Equal(t, "personal", skills["mine"].Scope)
}

func TestRefreshSessionSkills_PicksUpNewSkillsWithoutANewSession(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	srv := newSkillsServer(t, home, nil)
	id := createSkillsSession(t, srv, workspace)
	require.NotContains(t, sessionSkills(t, srv, id), "added-later")

	writeTestSkill(t, filepath.Join(workspace, ".agents", "skills"), "added-later", "added-later", "Created while the session was open")
	assert.NotContains(t, sessionSkills(t, srv, id), "added-later", "a session keeps the skills it started with until refreshed")

	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, localReq(http.MethodPost, "/sessions/"+id+"/skills/refresh", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var refreshed struct {
		Skills int `json:"skills"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&refreshed))
	assert.Equal(t, 1, refreshed.Skills)

	after := sessionSkills(t, srv, id)
	require.Contains(t, after, "added-later")
	assert.Equal(t, "project", after["added-later"].Scope)
}

func TestRefreshSessionSkills_UnknownSessionIsNotFound(t *testing.T) {
	srv := newSkillsServer(t, t.TempDir(), nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, localReq(http.MethodPost, "/sessions/does-not-exist/skills/refresh", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}
