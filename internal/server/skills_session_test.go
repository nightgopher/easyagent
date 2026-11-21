package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/nightgopher/easyagent/sdk/slashcmd"
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

func TestSkillSlashCommandRefreshesNewProjectSkill(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	srv := newSkillsServer(t, home, nil)
	srv.slashCmds = slashcmd.NewRegistry()
	id := createSkillsSession(t, srv, workspace)
	writeTestSkill(t, filepath.Join(workspace, ".agents", "skills"), "audit", "audit", "Audit the project")
	body := bytes.NewBufferString(`{"command":"/audit 检查配置"}`)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, localReq(http.MethodPost, "/sessions/"+id+"/command", body))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var result CommandResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&result))
	require.True(t, result.ShouldQuery)
	require.Equal(t, "audit", result.Skill)
	require.Equal(t, "/audit 检查配置", result.QueryPrompt)
	sess, err := srv.resolveSession(t.Context(), id)
	require.NoError(t, err)
	message, err := srv.buildPromptMessage(sess, result.QueryPrompt, promptInputs{Skill: result.Skill})
	require.NoError(t, err)
	require.Equal(t, result.QueryPrompt, message.DisplayText)
	require.Contains(t, message.Content[0].Text, `<skill name="audit"`)
	require.Contains(t, message.Content[0].Text, "检查配置")
}

func TestLocalSkillSuggestionsIncludeProjectBeforeSessionCreation(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	writeTestSkill(t, filepath.Join(home, ".agents", "skills"), "shared", "shared", "Personal")
	writeTestSkill(t, filepath.Join(workspace, ".agents", "skills"), "shared", "shared", "Project")
	srv := newSkillsServer(t, home, nil)
	w := httptest.NewRecorder()
	req := localReq(http.MethodGet, "/skills/local", nil)
	q := req.URL.Query()
	q.Set("workspace", workspace)
	req.URL.RawQuery = q.Encode()
	srv.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var out localSkillsResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	require.Len(t, out.Skills, 2)
	require.Equal(t, "Project", out.Skills[0].Description)
	require.Empty(t, out.Skills[0].ShadowedBy)
	require.Equal(t, out.Skills[0].Path, out.Skills[1].ShadowedBy)
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
