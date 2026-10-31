package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

func TestCodingDiscoveryFiltersPromptAndPreservesSkillMetadata(t *testing.T) {
	for _, mode := range []string{"off", "local"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("EA_HOME", t.TempDir())
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" {
					t.Errorf("unexpected provider call: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				fmt.Fprint(w, `{"data":[]}`)
			}))
			defer provider.Close()
			cfg := config.Default()
			cfg.DataDir, cfg.Workspace = t.TempDir(), t.TempDir()
			cfg.Provider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "fixture", provider.URL
			cfg.ToolSearch, cfg.EnableWeb, cfg.SkillMarketEnabled = mode, true, false
			skills := t.TempDir()
			path := filepath.Join(skills, "SKILL.md")
			require.NoError(t, os.WriteFile(path, []byte("---\nname: fixture-skill\ndescription: Fixture skill metadata stays visible.\n---\nSECRET_BODY_ONLY_READ_ON_DEMAND\n"), 0600))
			a, err := New(AppOptions{Config: cfg, SkillDirs: []string{skills}})
			require.NoError(t, err)
			defer a.Close()
			s, err := a.LoadOrCreateSession(context.Background(), "")
			require.NoError(t, err)
			snapshot, err := s.ContextSnapshot(context.Background())
			require.NoError(t, err)
			require.Contains(t, snapshot.System, "Fixture skill metadata stays visible.")
			require.Contains(t, snapshot.System, path)
			require.NotContains(t, snapshot.System, "SECRET_BODY_ONLY_READ_ON_DEMAND")
			var names []string
			for _, tool := range snapshot.Tools {
				names = append(names, tool.Name)
			}
			require.Contains(t, names, "read")
			u := snapshot.Usage.ToolDiscovery
			require.Equal(t, len(snapshot.Tools), u.Resident+u.Loaded)
			require.Equal(t, snapshot.Usage.Tools, u.DefinitionTokens)
			if mode == "local" {
				require.NotContains(t, snapshot.System, "### web_fetch")
				require.NotContains(t, snapshot.System, "The URL to fetch (http or https).")
				require.NotContains(t, names, "web_fetch")
				require.Contains(t, names, agent.SearchToolsName)
			} else {
				require.Contains(t, snapshot.System, "### web_fetch")
				require.Contains(t, names, "web_fetch")
				require.NotContains(t, names, agent.SearchToolsName)
			}
		})
	}
}
