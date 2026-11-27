package coding

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

func TestModelDiscoveryOnlyProbesConfiguredOpenAIProvider(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	}))
	defer server.Close()
	for _, provider := range []string{"", "anthropic", "codex", "deepv"} {
		cfg := config.Default()
		cfg.DataDir, cfg.Provider, cfg.OpenAIBaseURL = t.TempDir(), provider, server.URL
		_ = NewCodingApplication(cfg)
		require.Zero(t, requests.Load(), "provider %q should not probe an OpenAI gateway", provider)
	}
	cfg := config.Default()
	cfg.DataDir, cfg.Provider, cfg.OpenAIBaseURL = t.TempDir(), "openai", server.URL
	_ = NewCodingApplication(cfg)
	require.EqualValues(t, 1, requests.Load(), "explicit OpenAI gateway still supports discovery")
}
