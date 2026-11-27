package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nightgopher/easyagent/internal/agents/coding"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

func TestDesktopBootstrapAllowsOnlyMissingProvider(t *testing.T) {
	ctx := context.Background()
	var requests atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer gateway.Close()
	cfg := config.Default()
	cfg.DataDir, cfg.Workspace, cfg.OpenAIBaseURL = t.TempDir(), t.TempDir(), gateway.URL

	_, err := New(AppOptions{Config: cfg})
	require.ErrorContains(t, err, "EA_PROVIDER is not set")
	application, err := New(AppOptions{Config: cfg, AllowUnconfiguredProvider: true, Application: coding.NewCodingApplication(cfg)})
	require.NoError(t, err)
	defer application.Close()
	require.Zero(t, requests.Load(), "unconfigured startup must not probe a gateway")
	session, err := application.NewSession(ctx)
	require.NoError(t, err)
	_, err = session.Prompt(ctx, "hello")
	require.ErrorContains(t, err, "尚未配置模型服务")
	require.Zero(t, requests.Load(), "an unconfigured prompt must not contact a gateway")

	cfg.Provider, cfg.OpenAIAPIKey = "openai", ""
	_, err = New(AppOptions{Config: cfg, AllowUnconfiguredProvider: true})
	require.ErrorContains(t, err, "OPENAI_API_KEY is empty")
	cfg.Provider = "invalid"
	_, err = New(AppOptions{Config: cfg, AllowUnconfiguredProvider: true})
	require.ErrorContains(t, err, "unknown EA_PROVIDER")
}
