package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/internal/app"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

// A complete local session exercises provider registration, per-user catalog,
// stream conversion and the persisted model across a reopen.
func TestEasyCodeAccountRunsAnAgentSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer own-account-token" {
			t.Errorf("missing user's credential at %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/web-api/models":
			fmt.Fprint(w, `{"success":true,"data":[{"name":"deepseek-flash","displayName":"DeepSeek","available":true,"supportsImages":true}]}`)
		case "/v1/chat/stream":
			var body struct {
				Model  string `json:"model"`
				Config struct {
					MaxTokens int `json:"maxOutputTokens"`
				} `json:"config"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "deepseek-flash", body.Model)
			require.LessOrEqual(t, body.Config.MaxTokens, 32000)
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello from account\"}]},\"finishReason\":\"STOP\"}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	home := t.TempDir()
	t.Setenv("EA_HOME", home)
	t.Setenv("EA_EASYCODE_BASE_URL", server.URL)
	dir := filepath.Join(home, "easycode")
	require.NoError(t, os.Mkdir(dir, 0700))
	creds := fmt.Sprintf(`{"accessToken":"own-account-token","expiresAt":%d}`, time.Now().Add(time.Hour).UnixMilli())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(creds), 0600))
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.Provider = "deepv"
	cfg.OpenAIModel = "deepseek-flash"
	cfg.SkillMarketEnabled = false
	application, err := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, err)
	defer application.Close()
	w := httptest.NewRecorder()
	New(application, nil).Handler().ServeHTTP(w, localReq(http.MethodGet, "/models", nil))
	var catalog ModelsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &catalog))
	require.Equal(t, "easycode", catalog.Source)
	require.Len(t, catalog.Models, 1)
	require.Equal(t, "deepv", catalog.Models[0].Provider)
	ctx := context.Background()
	session, err := application.NewSession(ctx)
	require.NoError(t, err)
	id := session.SessionID()
	message, err := session.Prompt(ctx, "hello")
	require.NoError(t, err)
	require.Equal(t, "hello from account", message.Text)
	require.NoError(t, session.Close())
	session, err = application.LoadSession(ctx, id)
	require.NoError(t, err)
	provider, model := session.ModelInfo()
	require.Equal(t, "deepv", provider)
	require.Equal(t, "deepseek-flash", model)
}
