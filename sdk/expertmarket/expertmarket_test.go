package expertmarket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// catalogJSON is a mixed catalog: two valid experts, one valid team, and a
// handful of entries that the sanitizer must drop.
const catalogJSON = `{
  "meta": {"source": "synced", "syncedAt": "2026-11-10T06:15:26.172Z", "commit": "cc57fcbd"},
  "items": [
    {
      "id": "fullstack", "kind": "expert", "category": "technology", "icon": "code",
      "name": {"zh": "全栈开发专家", "en": "Full-stack developer"},
      "description": {"zh": "端到端交付。", "en": "End-to-end delivery."},
      "instructions": {"zh": "你是全栈开发专家。", "en": "You are a full-stack developer."},
      "skills": ["web-dev", "code-review"],
      "tags": [{"zh": "开发", "en": "Dev"}, {"zh": "", "en": "empty-zh"}]
    },
    {
      "id": "review", "kind": "expert", "category": "technology", "icon": "unknown-icon",
      "name": {"zh": "代码审查专家", "en": "Code reviewer"},
      "description": {"zh": "审查代码。", "en": "Review code."},
      "instructions": {"zh": "你是代码审查专家。", "en": "You are a code reviewer."}
    },
    {
      "id": "Bad_ID", "kind": "expert", "category": "technology", "icon": "code",
      "name": {"zh": "坏ID", "en": "Bad id"},
      "description": {"zh": "d", "en": "d"},
      "instructions": {"zh": "i", "en": "i"}
    },
    {
      "id": "no-en", "kind": "expert", "category": "technology", "icon": "code",
      "name": {"zh": "缺英文", "en": ""},
      "description": {"zh": "d", "en": "d"},
      "instructions": {"zh": "i", "en": "i"}
    },
    {
      "id": "dev-team", "kind": "team", "category": "technology", "icon": "shapes",
      "name": {"zh": "全栈专家团", "en": "Full-stack team"},
      "description": {"zh": "团队协作。", "en": "Team delivery."},
      "roles": {"zh": "全栈（负责人）、审查", "en": "Full-stack (lead), review"},
      "leader": "fullstack",
      "members": ["review", "review", "ghost"]
    },
    {
      "id": "ok-team", "kind": "team", "category": "technology", "icon": "shapes",
      "name": {"zh": "审查专家团", "en": "Review team"},
      "description": {"zh": "双人团队。", "en": "Two-person team."},
      "roles": {"zh": "全栈（负责人）、审查", "en": "Full-stack (lead), review"},
      "leader": "fullstack",
      "members": ["review"]
    },
    {
      "id": "self-team", "kind": "team", "category": "office", "icon": "shapes",
      "name": {"zh": "自环团队", "en": "Self team"},
      "description": {"zh": "d", "en": "d"},
      "roles": {"zh": "r", "en": "r"},
      "leader": "review",
      "members": ["review"]
    }
  ]
}`

func newFakeCatalog(t *testing.T, body string, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("capabilities"); got != capabilities {
			t.Errorf("capabilities query = %q, want %q", got, capabilities)
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestClientTemplatesSanitizes(t *testing.T) {
	server, _ := newFakeCatalog(t, catalogJSON, http.StatusOK)
	client := NewClient(WithBaseURL(server.URL))

	catalog, err := client.Templates(context.Background())
	if err != nil {
		t.Fatalf("Templates: %v", err)
	}
	if catalog.Meta.Source != "synced" || catalog.Meta.Commit != "cc57fcbd" {
		t.Fatalf("meta not mapped: %+v", catalog.Meta)
	}

	byID := make(map[string]Template, len(catalog.Items))
	for _, item := range catalog.Items {
		byID[item.ID] = item
	}
	// Bad_ID, no-en dropped; dev-team dropped (ghost member + dup member);
	// self-team dropped (leader is also a member).
	if len(catalog.Items) != 3 {
		t.Fatalf("got %d items, want 3: %+v", len(catalog.Items), catalog.Items)
	}

	fullstack := byID["fullstack"]
	if fullstack.Kind != KindExpert || fullstack.Category != "technology" || fullstack.Icon != "code" {
		t.Fatalf("fullstack base fields: %+v", fullstack)
	}
	// Valid skills survive; the empty-zh tag is dropped.
	if len(fullstack.Skills) != 2 || fullstack.Skills[0] != "web-dev" {
		t.Fatalf("fullstack skills: %v", fullstack.Skills)
	}
	if len(fullstack.Tags) != 1 || fullstack.Tags[0].En != "Dev" {
		t.Fatalf("fullstack tags: %+v", fullstack.Tags)
	}

	review := byID["review"]
	if review.Icon != "sparkle" {
		t.Fatalf("unknown icon should fall back to sparkle, got %q", review.Icon)
	}
	if len(review.Skills) != 0 {
		t.Fatalf("absent skills should stay nil, got %v", review.Skills)
	}

	team := byID["ok-team"]
	if team.Kind != KindTeam || team.Leader != "fullstack" || len(team.Members) != 1 || team.Members[0] != "review" {
		t.Fatalf("ok-team refs: %+v", team)
	}
	if team.Roles.Zh == "" || team.Roles.En == "" {
		t.Fatalf("ok-team roles: %+v", team.Roles)
	}
	if team.Instructions.Zh != "" {
		t.Fatalf("team should not carry instructions: %+v", team.Instructions)
	}
}

func TestClientTemplatesRejectsInvalidSkills(t *testing.T) {
	// An out-of-limit skills list voids the entry entirely.
	skills := `["a"`
	for i := 1; i < 33; i++ {
		skills += `, "s` + string(rune('a'+i%26)) + `"`
	}
	skills += `]`
	body := `{"items": [{
		"id": "skilled", "kind": "expert", "category": "life", "icon": "sparkle",
		"name": {"zh": "n", "en": "n"}, "description": {"zh": "d", "en": "d"},
		"instructions": {"zh": "i", "en": "i"},
		"skills": ` + skills + `
	}]}`
	server, _ := newFakeCatalog(t, body, http.StatusOK)
	client := NewClient(WithBaseURL(server.URL))

	catalog, err := client.Templates(context.Background())
	if err != nil {
		t.Fatalf("Templates: %v", err)
	}
	if len(catalog.Items) != 0 {
		t.Fatalf("oversized skills list should drop the entry, got %+v", catalog.Items)
	}
}

func TestClientTemplatesHTTPError(t *testing.T) {
	server, _ := newFakeCatalog(t, "boom", http.StatusInternalServerError)
	client := NewClient(WithBaseURL(server.URL))

	if _, err := client.Templates(context.Background()); err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}

func TestSourceKeepsLastGoodOnFailure(t *testing.T) {
	server, hits := newFakeCatalog(t, catalogJSON, http.StatusOK)
	client := NewClient(WithBaseURL(server.URL))

	now := time.Unix(1000000, 0)
	clock := now
	source := NewSource(client, WithNow(func() time.Time { return clock }))

	source.Refresh(context.Background())
	items, meta := source.Current()
	if len(items) != 3 || meta.Commit != "cc57fcbd" {
		t.Fatalf("first refresh: %d items, meta %+v", len(items), meta)
	}

	// A failing refresh keeps the last good list.
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	clock = clock.Add(refreshInterval)
	source.Refresh(context.Background())
	items, _ = source.Current()
	if len(items) != 3 {
		t.Fatalf("failed refresh should keep last good list, got %d items", len(items))
	}

	// A successful refresh replaces it.
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"items": [{"id": "solo", "kind": "expert", "category": "life", "icon": "sparkle", "name": {"zh": "n", "en": "n"}, "description": {"zh": "d", "en": "d"}, "instructions": {"zh": "i", "en": "i"}}]}`))
	})
	clock = clock.Add(refreshInterval)
	source.Refresh(context.Background())
	items, _ = source.Current()
	if len(items) != 1 || items[0].ID != "solo" {
		t.Fatalf("successful refresh should replace catalog, got %+v", items)
	}
}

func TestSourceRefreshSkipsFreshCatalog(t *testing.T) {
	server, hits := newFakeCatalog(t, catalogJSON, http.StatusOK)
	client := NewClient(WithBaseURL(server.URL))

	now := time.Unix(1000000, 0)
	clock := now
	source := NewSource(client, WithNow(func() time.Time { return clock }))

	source.Refresh(context.Background())
	// Within the interval the cache is considered fresh: no fetch, no replace.
	clock = clock.Add(refreshInterval - time.Second)
	source.Refresh(context.Background())
	if got := hits.Load(); got != 1 {
		t.Fatalf("fresh cache should not refetch, got %d hits", got)
	}

	// Past the interval it refreshes again.
	clock = clock.Add(time.Second)
	source.Refresh(context.Background())
	if got := hits.Load(); got != 2 {
		t.Fatalf("stale cache should refetch, got %d hits", got)
	}
}

func TestSanitizeEmptyCatalog(t *testing.T) {
	catalog := sanitizeCatalog(wireCatalog{})
	if catalog.Items == nil {
		t.Fatal("Items should be an empty slice, not nil")
	}
	if len(catalog.Items) != 0 {
		t.Fatalf("unexpected items: %+v", catalog.Items)
	}
}
