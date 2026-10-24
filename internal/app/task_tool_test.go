package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

func taskTestApp(t *testing.T, blocked []string) *App {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) }))
	t.Cleanup(provider.Close)
	cfg := config.Default()
	cfg.DataDir, cfg.Workspace = t.TempDir(), t.TempDir()
	cfg.Provider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "test-key", provider.URL
	cfg.BlockedTools = blocked
	a, err := New(AppOptions{Config: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { a.Close() })
	return a
}

func TestCodingSessionOffersTaskTool(t *testing.T) {
	sess, err := taskTestApp(t, nil).LoadOrCreateSession(context.Background(), "")
	require.NoError(t, err)
	require.Contains(t, sess.ToolNames(), "task")
}

func TestBlockedToolsRemoveTaskTool(t *testing.T) {
	sess, err := taskTestApp(t, []string{"task"}).LoadOrCreateSession(context.Background(), "")
	require.NoError(t, err)
	require.NotContains(t, sess.ToolNames(), "task")
}
