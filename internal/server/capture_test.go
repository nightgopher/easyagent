package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/nightgopher/easyagent/internal/app"
	"github.com/nightgopher/easyagent/internal/capture"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInboxHTTPPersistenceAndAuthentication(t *testing.T) {
	application := newTestApp(t)
	s := New(application, nil)
	h := s.Handler()
	record := capture.Record{ID: capture.NewID(), Text: "idea", Source: "desktop"}
	data, err := json.Marshal(record)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, localReq("POST", "/inbox", bytes.NewReader(data)))
		require.Equal(t, 200, w.Code, w.Body.String())
	}
	w := httptest.NewRecorder()
	New(application, nil).Handler().ServeHTTP(w, localReq("GET", "/inbox", nil))
	require.Equal(t, 200, w.Code)
	var got []capture.Record
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	w = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/inbox", bytes.NewReader(data))
	req.RemoteAddr = "203.0.113.10:1234"
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	record.Text = "conflict"
	data, err = json.Marshal(record)
	require.NoError(t, err)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, localReq("POST", "/inbox", bytes.NewReader(data)))
	require.Equal(t, 409, w.Code)
}
func TestArtifactsNeverOverwriteOrEscape(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir, cfg.Workspace = t.TempDir(), t.TempDir()
	cfg.Provider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "test", "http://127.0.0.1:1"
	application, appErr := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, appErr)
	t.Cleanup(func() { _ = application.Close() })
	s := New(application, nil)
	sess, err := s.resolveSession(context.Background(), "")
	require.NoError(t, err)
	post := func(path, content string) int {
		body, e := json.Marshal(map[string]string{"path": path, "content": content})
		require.NoError(t, e)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, localReq("POST", "/sessions/"+sess.SessionID()+"/artifacts", bytes.NewReader(body)))
		return w.Code
	}
	require.Equal(t, 200, post("notes/result.md", "# Result"))
	require.Equal(t, 409, post("notes/result.md", "overwrite"))
	data, err := os.ReadFile(filepath.Join(sess.Workspace(), "notes/result.md"))
	require.NoError(t, err)
	require.Equal(t, "# Result", string(data))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(sess.Workspace(), "escape")))
	for _, path := range []string{"../outside.md", filepath.Join(outside, "outside.md"), "escape/outside.md", "binary.exe"} {
		require.Equal(t, 400, post(path, "bad"), path)
	}
	_, err = os.Stat(filepath.Join(outside, "outside.md"))
	require.True(t, os.IsNotExist(err))
}
func TestKnowledgeReferencesStayInsideConfiguredRepo(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.KBRepoPath = t.TempDir()
	cfg.Provider = "openai"
	cfg.OpenAIAPIKey = "test"
	cfg.OpenAIBaseURL = "http://127.0.0.1:1"
	application, err := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { _ = application.Close() })
	s := New(application, nil)
	sess, err := s.resolveSession(context.Background(), "")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.KBRepoPath, "lesson.md"), []byte("knowledge content"), 0600))
	msg, err := s.buildPromptMessage(sess, "check", promptInputs{Files: []fileReference{{Path: "lesson.md", Workspace: sess.Workspace(), Scope: "knowledge"}}})
	require.NoError(t, err)
	require.Contains(t, msg.Content[1].Text, "knowledge content")
	require.NotContains(t, msg.DisplayText, "knowledge content")
	outside := filepath.Join(t.TempDir(), "secret.md")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(cfg.KBRepoPath, "escape.md")))
	for _, ref := range []fileReference{{Path: outside, Scope: "knowledge"}, {Path: "escape.md", Scope: "knowledge"}, {Path: "../secret.md", Scope: "knowledge"}, {Path: "lesson.md", Scope: "unexpected"}} {
		ref.Workspace = sess.Workspace()
		_, err := s.buildPromptMessage(sess, "check", promptInputs{Files: []fileReference{ref}})
		require.Error(t, err)
	}
}
