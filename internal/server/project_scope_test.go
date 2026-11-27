package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nightgopher/easyagent/internal/app"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newProjectScopeServer(t *testing.T, scope string, enableBash bool) (*Server, chan string) {
	t.Helper()
	home := realPath(t, t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("EA_ENABLE_TERMINAL", "1")
	workspace := filepath.Join(home, "parent", "project")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	for path, text := range map[string]string{
		filepath.Join(home, "AGENTS.md"):           "HOME_INSTRUCTION_MARKER",
		filepath.Join(home, "parent", "AGENTS.md"): "ANCESTOR_INSTRUCTION_MARKER",
		filepath.Join(workspace, "AGENTS.md"):      "PROJECT_INSTRUCTION_MARKER",
	} {
		require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	}
	writeTestSkill(t, filepath.Join(home, ".agents", "skills"), "home-only", "home-only", "HOME_SKILL_MARKER")
	writeTestSkill(t, filepath.Join(home, "parent", ".agents", "skills"), "ancestor-only", "ancestor-only", "ANCESTOR_SKILL_MARKER")
	writeTestSkill(t, filepath.Join(workspace, ".agents", "skills"), "unconfigured", "unconfigured", "UNCONFIGURED_SKILL_MARKER")
	skillRoot := filepath.Join(workspace, "expert-skills")
	skillDir := writeTestSkill(t, skillRoot, "mandatory", "mandatory", "Required project instructions")
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: mandatory\ndescription: Required project instructions\n---\n\nMANDATORY_SKILL_BODY_MARKER\n"), 0o600))
	writeTestSkill(t, skillRoot, "project-only", "project-only", "PROJECT_SKILL_MARKER")
	seen := make(chan string, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, message := range request.Messages {
			if message.Role == "system" {
				seen <- message.Content
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(provider.Close)
	cfg := config.Default()
	cfg.DataDir, cfg.Workspace = t.TempDir(), workspace
	cfg.Provider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "test-key", provider.URL
	cfg.ContextScope, cfg.EnableBash = scope, enableBash
	cfg.MCPConfigPath = filepath.Join(cfg.DataDir, "mcp.json")
	cfg.SkillMarketEnabled = false
	cfg.Skills = &config.SkillsConfig{Dirs: []string{skillRoot}, AlwaysInline: []string{"mandatory"}}
	cfg.FilePolicy = &config.FilePolicy{WriteAllow: []string{"content/**"}, WriteDeny: []string{"content/private/**"}}
	application, err := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, err)
	srv := New(application, nil)
	srv.SetAPIKey(workspaceSessionTestToken)
	t.Cleanup(func() { srv.cancel(); application.Close() })
	return srv, seen
}

func projectScopeRequest(t *testing.T, srv *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req := localReq(method, path, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+workspaceSessionTestToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

func createProjectScopeSession(t *testing.T, srv *Server, req CreateSessionRequest) string {
	t.Helper()
	w := projectScopeRequest(t, srv, http.MethodPost, "/sessions", req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var created SessionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)
	return created.ID
}

func TestProjectScopeSessionContextThroughCreateAPI(t *testing.T) {
	srv, seen := newProjectScopeServer(t, "project", false)
	id := createProjectScopeSession(t, srv, CreateSessionRequest{})
	sess, err := srv.app.LoadSession(t.Context(), id)
	require.NoError(t, err)
	var names []string
	for _, sk := range sess.LoadedSkills() {
		names = append(names, sk.Name)
	}
	assert.ElementsMatch(t, []string{"mandatory", "project-only"}, names)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = sess.Prompt(ctx, "hello")
	require.NoError(t, err)
	select {
	case system := <-seen:
		assert.Contains(t, system, "PROJECT_INSTRUCTION_MARKER")
		assert.Contains(t, system, "PROJECT_SKILL_MARKER")
		assert.Contains(t, system, "MANDATORY_SKILL_BODY_MARKER")
		for _, marker := range []string{"HOME_INSTRUCTION_MARKER", "ANCESTOR_INSTRUCTION_MARKER", "HOME_SKILL_MARKER", "ANCESTOR_SKILL_MARKER", "UNCONFIGURED_SKILL_MARKER"} {
			assert.NotContains(t, system, marker)
		}
	case <-ctx.Done():
		t.Fatal("mock provider did not receive the system context")
	}
	workspace, _ := srv.app.SessionManager().Metadata(id)
	assert.Equal(t, realPath(t, srv.app.Config().Workspace), workspace)
}

func TestProjectScopeMissingMandatorySkillRejectsCreateAPI(t *testing.T) {
	srv, _ := newProjectScopeServer(t, "project", false)
	path := filepath.Join(srv.app.Config().Skills.Dirs[0], "mandatory", "SKILL.md")
	require.NoError(t, os.Remove(path))
	w := projectScopeRequest(t, srv, http.MethodPost, "/sessions", CreateSessionRequest{})
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "mandatory")
	assert.Empty(t, srv.app.SessionStore().List(), "missing required instructions cannot yield a usable session")
}

func TestNonProjectScopeKeepsWorkspaceSelection(t *testing.T) {
	for _, scope := range []string{"", "all"} {
		t.Run("scope="+scope, func(t *testing.T) {
			srv, _ := newProjectScopeServer(t, scope, false)
			workspace := realPath(t, t.TempDir())
			id := createProjectScopeSession(t, srv, CreateSessionRequest{Cwd: workspace})
			actual, _ := srv.app.SessionManager().Metadata(id)
			assert.Equal(t, workspace, actual)
		})
	}
}

func TestProjectScopeCreateSessionRejectsOverrides(t *testing.T) {
	srv, _ := newProjectScopeServer(t, "project", false)
	root := srv.app.Config().Workspace
	for _, req := range []CreateSessionRequest{
		{Cwd: t.TempDir()}, {Cwd: filepath.Dir(root)}, {Cwd: filepath.Join(root, "expert-skills")},
		{AgentID: "business-agent"}, {AgentID: "steward"}, {TeamID: "team"},
		{Application: "business"}, {Application: "kb"}, {Application: "unknown"},
		{ParentSessionID: "parent"},
	} {
		w := projectScopeRequest(t, srv, http.MethodPost, "/sessions", req)
		assert.Equal(t, http.StatusForbidden, w.Code, "request %+v: %s", req, w.Body.String())
	}
	assert.Empty(t, srv.app.SessionStore().List())
	alias := filepath.Join(t.TempDir(), "project-link")
	require.NoError(t, os.Symlink(root, alias))
	id := createProjectScopeSession(t, srv, CreateSessionRequest{Cwd: alias, Application: "coding"})
	workspace, application := srv.app.SessionManager().Metadata(id)
	assert.Equal(t, root, workspace)
	assert.Equal(t, "coding", application)
}

func TestAPIFileWritesRespectConfiguredPolicy(t *testing.T) {
	for _, scope := range []string{"", "project"} {
		t.Run("scope="+scope, func(t *testing.T) {
			srv, _ := newProjectScopeServer(t, scope, false)
			id := createProjectScopeSession(t, srv, CreateSessionRequest{})
			root := srv.app.Config().Workspace
			require.NoError(t, os.MkdirAll(filepath.Join(root, "content"), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(root, "protected"), 0o755))
			require.NoError(t, os.Symlink(filepath.Join(root, "protected"), filepath.Join(root, "content", "link")))
			for _, endpoint := range []string{"/sessions/" + id + "/file?", "/workspace/write-file?", "/workspace/write-file?session_id=" + id + "&"} {
				for _, path := range []string{"config/new.txt", "content/private/new.txt", "content/link/new.txt"} {
					w := projectScopeRequest(t, srv, http.MethodPut, endpoint+"path="+url.QueryEscape(path), writeFileRequest{Content: "denied"})
					assert.Equal(t, http.StatusForbidden, w.Code, "%s %s: %s", endpoint, path, w.Body.String())
				}
				w := projectScopeRequest(t, srv, http.MethodPut, endpoint+"path=content/new/note.md", writeFileRequest{Content: "allowed"})
				assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
			}
			for _, path := range []string{"config", "content/private", "protected/new.txt"} {
				_, err := os.Stat(filepath.Join(root, path))
				assert.True(t, os.IsNotExist(err), "rejected writes must not create files or directories: %s", path)
			}
			data, err := os.ReadFile(filepath.Join(root, "content/new/note.md"))
			require.NoError(t, err)
			assert.Equal(t, "allowed", string(data))
		})
	}
}

func TestProjectScopeWorkspaceRejectsPersistedOutsideSession(t *testing.T) {
	srv, _ := newProjectScopeServer(t, "project", false)
	manager := srv.app.SessionManager()
	id, _, err := manager.Create(t.Context())
	require.NoError(t, err)
	outside := t.TempDir()
	require.NoError(t, manager.SaveMeta(id, outside, "coding"))
	for _, endpoint := range workspaceSessionEndpoints {
		w := projectScopeRequest(t, srv, endpoint.method, "/workspace/"+endpoint.endpoint+"?session_id="+id+"&path=secret.txt", writeFileRequest{Content: "denied"})
		assert.Equal(t, http.StatusForbidden, w.Code, "%s: %s", endpoint.endpoint, w.Body.String())
	}
	assert.Empty(t, srv.app.SessionStore().List(), "do not load an out-of-project session to resolve workspace APIs")
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodGet, "/sessions/" + id + "/file?path=secret.txt"},
		{http.MethodPut, "/sessions/" + id + "/file?path=content/new.txt"},
		{http.MethodGet, "/sessions/" + id + "/diff"},
	} {
		w := projectScopeRequest(t, srv, endpoint.method, endpoint.path, writeFileRequest{Content: "denied"})
		assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	}
	_, err = os.Stat(filepath.Join(outside, "content"))
	assert.True(t, os.IsNotExist(err))
}

func TestProjectScopeTerminalCannotBeEnabled(t *testing.T) {
	for _, enableBash := range []bool{false, true} {
		t.Run(fmt.Sprintf("bash=%t", enableBash), func(t *testing.T) {
			srv, _ := newProjectScopeServer(t, "project", enableBash)
			require.True(t, srv.terminalEnabled)
			id := createProjectScopeSession(t, srv, CreateSessionRequest{})
			host := httptest.NewServer(srv.Handler())
			defer host.Close()
			conn, response, err := websocket.DefaultDialer.Dial(strings.Replace(host.URL, "http", "ws", 1)+"/terminal?session_id="+id, http.Header{"Authorization": {"Bearer " + workspaceSessionTestToken}})
			if conn != nil {
				conn.Close()
			}
			require.Error(t, err)
			require.NotNil(t, response)
			defer response.Body.Close()
			assert.Equal(t, http.StatusForbidden, response.StatusCode)
			assert.Empty(t, srv.terminalSlots, "project requests never reach PTY admission")
		})
	}
}
