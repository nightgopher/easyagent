package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nightgopher/easyagent/internal/app"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modelTestApp(t *testing.T, configure func(*config.Config)) *app.App {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Provider = "openai"
	cfg.OpenAIAPIKey = "test-key"
	configure(&cfg)
	application, err := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { application.Close() })
	return application
}

func TestModelCatalogFailureNeverInventsModels(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("sensitive upstream error"))
	}))
	defer gateway.Close()
	application := modelTestApp(t, func(cfg *config.Config) {
		cfg.OpenAIBaseURL = gateway.URL
		cfg.OpenAIModel = "my-configured-model"
	})
	srv := New(application, nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, localReq(http.MethodGet, "/models", nil))
	var catalog ModelsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &catalog))
	assert.Equal(t, "configured", catalog.Source)
	require.Len(t, catalog.Models, 1)
	assert.Equal(t, "my-configured-model", catalog.Models[0].ID)
	assert.Equal(t, "model discovery returned HTTP 401", catalog.DiscoveryError)
	assert.NotContains(t, w.Body.String(), "sensitive")
}

func TestModelDiscoveryDoesNotForwardCredentialsThroughRedirect(t *testing.T) {
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected++
		_, _ = w.Write([]byte(`{"data":[{"id":"unexpected"}]}`))
	}))
	defer target.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/models", http.StatusFound)
	}))
	defer gateway.Close()
	models, err := (&Server{}).fetchGatewayModels(context.Background(), gateway.URL, "test-key")
	require.Error(t, err)
	assert.Nil(t, models)
	assert.Equal(t, 0, redirected)
}
