package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hwj123hwj/easyagent/sdk/slashcmd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f workspaceSessionFixture) sessionRequest(method, endpoint, body string) *httptest.ResponseRecorder {
	req := localReq(method, "/sessions/"+f.id+endpoint, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+workspaceSessionTestToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	return w
}

func TestSessionFileRoundTripStaysInsideTheProject(t *testing.T) {
	f := newWorkspaceSessionFixture(t)

	w := f.sessionRequest(http.MethodPut, "/file?path=new/dir/note.txt", `{"content":"session note"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	data, err := os.ReadFile(filepath.Join(f.project, "new/dir/note.txt"))
	require.NoError(t, err)
	assert.Equal(t, "session note", string(data))

	w = f.sessionRequest(http.MethodGet, "/file?path=new/dir/note.txt", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"content":"session note"}`, w.Body.String())

	escape := filepath.Join("..", "outside", "secret.txt")
	w = f.sessionRequest(http.MethodPut, "/file?path="+escape, `{"content":"overwritten"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w = f.sessionRequest(http.MethodGet, "/file?path="+escape, "")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	secret, err := os.ReadFile(filepath.Join(f.outside, "secret.txt"))
	require.NoError(t, err)
	assert.Equal(t, "outside content", string(secret))

	assert.Equal(t, http.StatusBadRequest, f.sessionRequest(http.MethodPut, "/file", `{"content":"x"}`).Code)
	assert.Equal(t, http.StatusBadRequest, f.sessionRequest(http.MethodPut, "/file?path=a.txt", `not json`).Code)
	assert.Equal(t, http.StatusBadRequest, f.sessionRequest(http.MethodGet, "/file", "").Code)
	assert.Equal(t, http.StatusNotFound, f.sessionRequest(http.MethodGet, "/file?path=missing.txt", "").Code)
}

func TestSessionDiffReportsUncommittedChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newWorkspaceSessionFixture(t)

	// Outside a repository the diff is empty rather than an error.
	w := f.sessionRequest(http.MethodGet, "/diff", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"files":[]}`, w.Body.String())

	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.invalid"}, args...)...)
		cmd.Dir = f.project
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "initial")
	require.NoError(t, os.WriteFile(filepath.Join(f.project, "hello.txt"), []byte("changed project\nsecond line\n"), 0o644))

	w = f.sessionRequest(http.MethodGet, "/diff", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Files []fileDiff `json:"files"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Files, 1)
	assert.Equal(t, "hello.txt", body.Files[0].Path)
	assert.Equal(t, 2, body.Files[0].Added)
	assert.Equal(t, 1, body.Files[0].Removed)
	assert.Contains(t, body.Files[0].Patch, "diff --git a/hello.txt b/hello.txt")
}

func TestExecuteCommandRunsSlashCommands(t *testing.T) {
	f := newWorkspaceSessionFixture(t)
	assert.Equal(t, http.StatusNotImplemented, f.sessionRequest(http.MethodPost, "/command", `{"command":"/ping"}`).Code)

	registry := slashcmd.NewRegistry()
	registry.Register(slashcmd.Command{Name: "ping", Handler: func(ctx slashcmd.Context, args string) (slashcmd.CommandResult, error) {
		return slashcmd.CommandResult{Output: "pong " + args, ShouldQuery: args == "go"}, nil
	}})
	srv := New(f.app, registry)
	srv.SetAPIKey(workspaceSessionTestToken)
	f.handler = srv.Handler()

	w := f.sessionRequest(http.MethodPost, "/command", `{"command":"/ping go"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp CommandResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, CommandResponse{Output: "pong go", ShouldQuery: true, QueryPrompt: "Start working..."}, resp)

	assert.Equal(t, http.StatusBadRequest, f.sessionRequest(http.MethodPost, "/command", `{"command":""}`).Code)
	assert.Equal(t, http.StatusBadRequest, f.sessionRequest(http.MethodPost, "/command", `not json`).Code)
	assert.Equal(t, http.StatusBadRequest, f.sessionRequest(http.MethodPost, "/command", `{"command":"/unknown"}`).Code)
}

func TestRegisterToolReplacesByNameAndReachesTheApp(t *testing.T) {
	srv := New(newTestAppWithWorkspace(t, t.TempDir()), nil)
	handler := srv.Handler()
	register := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, localReq(http.MethodPost, "/tools/register", bytes.NewReader([]byte(body))))
		return w
	}

	w := register(`{"name":"lookup","description":"first","callback_url":"http://127.0.0.1:9/cb","allow_private":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = register(`{"name":"lookup","description":"second","callback_url":"http://127.0.0.1:9/cb","allow_private":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	tools := srv.ExternalTools()
	require.Len(t, tools, 1)
	assert.Equal(t, "second", tools[0].Description)

	for _, body := range []string{`not json`, `{"callback_url":"http://127.0.0.1:9/cb"}`, `{"name":"x"}`, `{"name":"x","callback_url":"http://127.0.0.1:9/cb"}`} {
		assert.Equal(t, http.StatusBadRequest, register(body).Code, body)
	}
	assert.Len(t, srv.ExternalTools(), 1)

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, localReq(http.MethodGet, "/applications", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"applications"`)
}
