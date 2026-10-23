package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/hwj123hwj/easyagent/internal/app"
	"github.com/hwj123hwj/easyagent/sdk/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newKBTestServer(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	base := realPath(t, t.TempDir())
	repo, outside := filepath.Join(base, "kb"), filepath.Join(base, "outside")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "issues"), 0o750))
	require.NoError(t, os.MkdirAll(outside, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "issues", "lesson.md"), []byte("---\ntitle: Lesson\n---\nbody\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside secret"), 0o600))
	cfg := config.Default()
	cfg.DataDir, cfg.Workspace, cfg.KBRepoPath = t.TempDir(), t.TempDir(), repo
	cfg.Provider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "test-key", "http://localhost:4001"
	application, err := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { _ = application.Close() })
	return New(application, nil).Handler(), repo, outside
}

func kbRead(handler http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, localReq(http.MethodGet, "/kb/read?path="+url.QueryEscape(path), nil))
	return w
}

func TestKBReadStaysInsideTheKnowledgeBase(t *testing.T) {
	handler, repo, outside := newKBTestServer(t)

	w := kbRead(handler, "issues/lesson.md")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body kbReadResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Contains(t, body.Content, "title: Lesson")

	require.NoError(t, os.Symlink(outside, filepath.Join(repo, "escape")))
	for _, path := range []string{"escape/secret.txt", filepath.Join(repo, "escape", "secret.txt"), "../outside/secret.txt", filepath.Join(outside, "secret.txt")} {
		w := kbRead(handler, path)
		assert.Equal(t, http.StatusForbidden, w.Code, path)
		assert.NotContains(t, w.Body.String(), "outside secret", path)
	}
	assert.Equal(t, http.StatusBadRequest, kbRead(handler, "").Code)
}

func kbGet(t *testing.T, handler http.Handler, target string, out any) {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, localReq(http.MethodGet, target, nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), out))
}

func TestKBBrowserEndpointsSummariseTheIndex(t *testing.T) {
	handler, repo, _ := newKBTestServer(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "docs"), 0o750))
	for path, content := range map[string]string{
		"issues/lint.md":   "---\ntitle: Lint baseline\nsummary: shrink only\ntags: [go, lint]\n---\nbody\n",
		"docs/untitled.md": "no frontmatter here\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(repo, path), []byte(content), 0o600))
	}

	var stats kbStatsResponse
	kbGet(t, handler, "/kb/stats", &stats)
	assert.Equal(t, 3, stats.TotalEntries)
	assert.Equal(t, 2, stats.Categories)
	assert.Equal(t, repo, stats.RepoPath)

	var entries kbEntriesResponse
	kbGet(t, handler, "/kb/entries?tag=GO", &entries)
	require.Equal(t, 1, entries.Total)
	assert.Equal(t, "Lint baseline", entries.Entries[0].Title)
	kbGet(t, handler, "/kb/entries?category=docs", &entries)
	assert.Equal(t, 1, entries.Total)
	kbGet(t, handler, "/kb/entries?q=baseline", &entries)
	require.NotZero(t, entries.Total)
	assert.Equal(t, "Lint baseline", entries.Entries[0].Title)
	kbGet(t, handler, "/kb/entries?q=nothing-matches-this", &entries)
	assert.Equal(t, kbEntriesResponse{Entries: []kbEntryJSON{}}, entries)

	var categories kbCategoriesResponse
	kbGet(t, handler, "/kb/categories", &categories)
	assert.Len(t, categories.Categories, 2)
	var tags kbTagsResponse
	kbGet(t, handler, "/kb/tags", &tags)
	assert.Contains(t, tags.Tags, kbTagJSON{Name: "go", Count: 1})

	var health kbHealthResponse
	kbGet(t, handler, "/kb/health", &health)
	assert.Equal(t, 3, health.TotalEntries)
	assert.NotEmpty(t, health.EntriesMissingTags)
	assert.NotNil(t, health.DuplicateGroups)
}
