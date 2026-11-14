package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nightgopher/easyagent/internal/app"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

// expertMarketCatalog mirrors the marketplace wire format closely enough for
// the sanitizer: two experts plus one team referencing both.
const expertMarketCatalog = `{
  "meta": {"source": "synced", "syncedAt": "2026-11-10T06:15:26.172Z", "commit": "cc57fcbd"},
  "items": [
    {
      "id": "fullstack", "kind": "expert", "category": "technology", "icon": "code",
      "name": {"zh": "全栈开发专家", "en": "Full-stack developer"},
      "description": {"zh": "端到端交付。", "en": "End-to-end delivery."},
      "instructions": {"zh": "你是全栈开发专家。", "en": "You are a full-stack developer."},
      "skills": ["web-dev"]
    },
    {
      "id": "review", "kind": "expert", "category": "technology", "icon": "review",
      "name": {"zh": "代码审查专家", "en": "Code reviewer"},
      "description": {"zh": "审查代码。", "en": "Review code."},
      "instructions": {"zh": "你是代码审查专家。", "en": "You are a code reviewer."}
    },
    {
      "id": "dev-team", "kind": "team", "category": "technology", "icon": "shapes",
      "name": {"zh": "全栈专家团", "en": "Full-stack team"},
      "description": {"zh": "双人协作。", "en": "Pair delivery."},
      "roles": {"zh": "全栈（负责人）、审查", "en": "Full-stack (lead), review"},
      "leader": "fullstack",
      "members": ["review"]
    }
  ]
}`

// newExpertMarketTestServer builds a server whose expert catalog comes from a
// stub upstream, returning the server and the upstream hit counter.
func newExpertMarketTestServer(t *testing.T, body string, status int) (*Server, *atomic.Int32, *httptest.Server) {
	t.Helper()
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Default()
	cfg.DataDir, cfg.Workspace = t.TempDir(), t.TempDir()
	cfg.Provider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "test-key", "http://127.0.0.1:1"
	cfg.ExpertMarketBaseURL = upstream.URL
	application, err := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { _ = application.Close() })
	return New(application, nil), &hits, upstream
}

func TestExpertMarketTemplates(t *testing.T) {
	s, hits, _ := newExpertMarketTestServer(t, expertMarketCatalog, http.StatusOK)

	w := businessRequest(t, s, "GET", "/experts/market/templates", nil, 200)
	var out struct {
		Items []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"items"`
		Meta struct {
			Commit string `json:"commit"`
		} `json:"meta"`
		Installed []map[string]any `json:"installed"`
		Error     string           `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Len(t, out.Items, 3)
	require.Equal(t, "cc57fcbd", out.Meta.Commit)
	require.Empty(t, out.Installed)
	require.Empty(t, out.Error)
	require.EqualValues(t, 1, hits.Load())

	// Second fetch within the refresh interval is served from cache.
	businessRequest(t, s, "GET", "/experts/market/templates", nil, 200)
	require.EqualValues(t, 1, hits.Load())
}

func TestExpertMarketInstallExpert(t *testing.T) {
	s, _, _ := newExpertMarketTestServer(t, expertMarketCatalog, http.StatusOK)

	w := businessRequest(t, s, "POST", "/experts/market/install",
		map[string]any{"id": "fullstack", "kind": "expert"}, 200)
	var out struct {
		Installed bool `json:"installed"`
		Agents    []struct {
			ID           string   `json:"id"`
			Name         string   `json:"name"`
			Instructions string   `json:"instructions"`
			Skills       []string `json:"skills"`
			MemoryEnabled bool    `json:"memory_enabled"`
		} `json:"agents"`
		MissingSkills []string `json:"missing_skills"`
		Team          any      `json:"team"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.True(t, out.Installed)
	require.Len(t, out.Agents, 1)
	require.Equal(t, "全栈开发专家", out.Agents[0].Name)
	require.Contains(t, out.Agents[0].Instructions, "端到端交付。")
	require.Contains(t, out.Agents[0].Instructions, "你是全栈开发专家。")
	require.Equal(t, []string{"web-dev"}, out.Agents[0].Skills)
	require.True(t, out.Agents[0].MemoryEnabled)
	require.Nil(t, out.Team)
	require.NotNil(t, out.MissingSkills)
	require.Contains(t, out.MissingSkills, "web-dev") // test host has no global skills

	// The created agent is a first-class business agent.
	w = businessRequest(t, s, "GET", "/agents/"+out.Agents[0].ID, nil, 200)
	require.Contains(t, w.Body.String(), "全栈开发专家")

	// The install is recorded for the market badge.
	w = businessRequest(t, s, "GET", "/experts/market/templates", nil, 200)
	require.Contains(t, w.Body.String(), `"template_id":"fullstack"`)

	// Re-install produces a fresh agent id.
	first := out.Agents[0].ID
	w = businessRequest(t, s, "POST", "/experts/market/install",
		map[string]any{"id": "fullstack", "kind": "expert"}, 200)
	require.NotContains(t, w.Body.String(), first)
}

func TestExpertMarketInstallTeam(t *testing.T) {
	s, _, _ := newExpertMarketTestServer(t, expertMarketCatalog, http.StatusOK)

	w := businessRequest(t, s, "POST", "/experts/market/install",
		map[string]any{"id": "dev-team", "kind": "team"}, 200)
	var out struct {
		Agents []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"agents"`
		Team struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			LeaderID string `json:"leader_id"`
			Members  []struct {
				AgentID string `json:"agent_id"`
			} `json:"members"`
		} `json:"team"`
		MissingSkills []string `json:"missing_skills"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Len(t, out.Agents, 2) // leader + member experts
	require.Equal(t, "全栈专家团", out.Team.Name)
	require.Equal(t, out.Agents[0].ID, out.Team.LeaderID)
	require.Len(t, out.Team.Members, 1)
	require.Equal(t, out.Agents[1].ID, out.Team.Members[0].AgentID)
	require.Contains(t, out.MissingSkills, "web-dev")

	// The team is persisted with member references that resolve.
	w = businessRequest(t, s, "GET", "/teams/"+out.Team.ID, nil, 200)
	require.Contains(t, w.Body.String(), out.Team.LeaderID)
}

func TestExpertMarketInstallValidation(t *testing.T) {
	s, _, _ := newExpertMarketTestServer(t, expertMarketCatalog, http.StatusOK)

	businessRequest(t, s, "POST", "/experts/market/install", map[string]any{"id": "", "kind": "expert"}, 400)
	businessRequest(t, s, "POST", "/experts/market/install", map[string]any{"id": "fullstack", "kind": "robot"}, 400)
	businessRequest(t, s, "POST", "/experts/market/install", map[string]any{"id": "fullstack", "kind": "expert", "locale": "fr"}, 400)
	businessRequest(t, s, "POST", "/experts/market/install", map[string]any{"id": "ghost", "kind": "expert"}, 404)
	businessRequest(t, s, "POST", "/experts/market/install", map[string]any{"id": "dev-team", "kind": "expert"}, 404) // kind mismatch
}

func TestExpertMarketUnavailableUpstream(t *testing.T) {
	s, _, _ := newExpertMarketTestServer(t, "boom", http.StatusInternalServerError)

	businessRequest(t, s, "GET", "/experts/market/templates", nil, 200) // empty catalog + error hint
	w := businessRequest(t, s, "POST", "/experts/market/install", map[string]any{"id": "fullstack", "kind": "expert"}, 502)
	require.Contains(t, w.Body.String(), "专家目录暂不可用")
}

func TestExpertMarketDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir, cfg.Workspace = t.TempDir(), t.TempDir()
	cfg.Provider, cfg.OpenAIAPIKey, cfg.OpenAIBaseURL = "openai", "test-key", "http://127.0.0.1:1"
	cfg.ExpertMarketEnabled = false
	application, err := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { _ = application.Close() })
	s := New(application, nil)

	w := businessRequest(t, s, "GET", "/experts/market/templates", nil, 404)
	require.Contains(t, w.Body.String(), "404")
	businessRequest(t, s, "POST", "/experts/market/install", map[string]any{"id": "x", "kind": "expert"}, 404)
}
