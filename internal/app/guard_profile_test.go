package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nightgopher/easyagent/sdk/config"
)

func guardProfileApp(t *testing.T, configure func(*config.Config)) *App {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) }))
	t.Cleanup(provider.Close)
	cfg := config.Default()
	cfg.DataDir, cfg.Workspace = t.TempDir(), t.TempDir()
	cfg.Provider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "test-key", provider.URL
	if configure != nil {
		configure(&cfg)
	}
	a, err := New(AppOptions{Config: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { a.Close() })
	return a
}

func TestSubmitToolIsRegisteredOnlyWithAGate(t *testing.T) {
	session := func(t *testing.T, a *App) []string {
		sess, err := a.LoadOrCreateSession(context.Background(), "")
		require.NoError(t, err)
		return sess.ToolNames()
	}

	plain := guardProfileApp(t, nil)
	require.NotContains(t, session(t, plain), "submit_task")
	require.NotContains(t, plain.ToolNames(), "submit_task")

	site := func(cfg *config.Config) {
		cfg.Submit = &config.SubmitConfig{Gate: []string{"true"}, Include: []string{"src/content/**"}}
	}
	guarded := guardProfileApp(t, site)
	require.Contains(t, session(t, guarded), "submit_task")
	require.Contains(t, guarded.ToolNames(), "submit_task")

	// The ordinary tool filters apply to it like to any other tool.
	blocked := guardProfileApp(t, func(cfg *config.Config) {
		site(cfg)
		cfg.BlockedTools = []string{"submit_task"}
	})
	require.NotContains(t, session(t, blocked), "submit_task")
}
