// Package expertmarket implements a client for the expert-template catalog
// served by the EasyCode marketplace (https://skills.deepvlab.ai).
//
// The catalog is curated in the easycode-skills-workspace GitHub repository
// (catalog/expert-templates.json) and synchronized to the marketplace server,
// the same content source that powers the skill store. Templates are strictly
// sanitized field by field before use: a template's instructions become a
// business agent's system prompt once installed, so malformed, oversized, or
// duplicative entries are dropped rather than surfaced. A team template may
// only reference expert templates within the same response.
package expertmarket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the EasyCode marketplace serving the expert catalog.
const DefaultBaseURL = "https://skills.deepvlab.ai"

// DefaultTimeout bounds every catalog HTTP request. The list changes rarely,
// so a slow endpoint should fail fast into the cached copy.
const DefaultTimeout = 5 * time.Second

// capabilities pins the wire contract the client understands.
const capabilities = "expert-profiles-v1"

// templatesPath is the catalog endpoint on the marketplace server.
const templatesPath = "/api/expert-templates"

// refreshInterval is how often Refresh re-fetches the catalog.
const refreshInterval = 10 * time.Minute

// Catalog limits, mirroring the reference client's sanitizer: anything a
// template could never save as an agent profile is rejected outright.
const (
	maxTemplates       = 100
	maxTags            = 8
	maxTagLen          = 40
	maxMembers         = 8
	maxSkills          = 32
	maxNameLen         = 100
	maxDescriptionLen  = 2000
	maxInstructionsLen = 32000
	maxRolesLen        = 500
)

// idPattern constrains template and skill identifiers.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// categories is the fixed template classification set.
var categories = map[string]bool{
	"technology": true,
	"office":     true,
	"creative":   true,
	"life":       true,
}

// icons is the fixed card icon set; unknown icons fall back to "sparkle".
var icons = map[string]bool{
	"code": true, "review": true, "plan": true, "file-text": true, "edit": true,
	"sparkle": true, "tasks": true, "search": true, "panel": true, "cpu": true,
	"flask": true, "comment": true, "shapes": true,
}

// ─── Catalog model ───────────────────────────────────────────────────────────

// Kind distinguishes role templates from team templates.
type Kind string

const (
	KindExpert Kind = "expert"
	KindTeam   Kind = "team"
)

// LocalizedText carries the zh/en pair required of every user-visible field.
type LocalizedText struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// Template is a sanitized catalog entry: a role expert or a curated team.
type Template struct {
	ID string `json:"id"`
	// Kind is KindExpert or KindTeam.
	Kind       Kind   `json:"kind"`
	Category   string `json:"category"`
	Icon       string `json:"icon"`
	Name       LocalizedText   `json:"name"`
	Description LocalizedText  `json:"description"`
	Tags       []LocalizedText `json:"tags,omitempty"`
	// Instructions is set for KindExpert: the role system prompt.
	Instructions LocalizedText `json:"instructions,omitempty"`
	// Skills is the optional expert skill binding (template ids of skills).
	Skills []string `json:"skills,omitempty"`
	// Roles, Leader and Members are set for KindTeam. Leader and Members
	// reference expert templates from the same catalog response.
	Roles   LocalizedText `json:"roles,omitempty"`
	Leader  string        `json:"leader,omitempty"`
	Members []string      `json:"members,omitempty"`
}

// Meta describes where the served catalog came from.
type Meta struct {
	Source   string `json:"source,omitempty"`
	SyncedAt string `json:"synced_at,omitempty"`
	Commit   string `json:"commit,omitempty"`
}

// Catalog is a sanitized template list plus its provenance.
type Catalog struct {
	Items []Template `json:"items"`
	Meta  Meta       `json:"meta"`
}

// ─── Wire format ─────────────────────────────────────────────────────────────

type wireLocalized struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

type wireItem struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Category     string          `json:"category"`
	Icon         string          `json:"icon"`
	Name         wireLocalized   `json:"name"`
	Description  wireLocalized   `json:"description"`
	Tags         []wireLocalized `json:"tags"`
	Instructions wireLocalized   `json:"instructions"`
	Skills       []string        `json:"skills"`
	Roles        wireLocalized   `json:"roles"`
	Leader       string          `json:"leader"`
	Members      []string        `json:"members"`
}

type wireCatalog struct {
	Items []wireItem `json:"items"`
	Meta  struct {
		Source   string `json:"source"`
		SyncedAt string `json:"syncedAt"`
		Commit   string `json:"commit"`
	} `json:"meta"`
}

// ─── Client ──────────────────────────────────────────────────────────────────

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the marketplace endpoint.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		c.baseURL = strings.TrimRight(u, "/")
	}
}

// WithHTTPClient overrides the underlying HTTP client (tests).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// Client fetches the expert-template catalog from the marketplace.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a catalog client.
func NewClient(opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		http:    &http.Client{Timeout: DefaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// BaseURL returns the configured marketplace endpoint.
func (c *Client) BaseURL() string { return c.baseURL }

// Templates fetches and sanitizes the catalog. A non-2xx reply, a network
// failure, or an unparsable body is an error; individual bad entries inside a
// parsable body are silently dropped by the sanitizer.
func (c *Client) Templates(ctx context.Context) (Catalog, error) {
	q := url.Values{"capabilities": []string{capabilities}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+templatesPath+"?"+q.Encode(), nil)
	if err != nil {
		return Catalog{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Catalog{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Catalog{}, fmt.Errorf("expertmarket: API error %d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(body)))
	}
	var raw wireCatalog
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Catalog{}, err
	}
	return sanitizeCatalog(raw), nil
}

// ─── Sanitizer ───────────────────────────────────────────────────────────────

// sanitizeCatalog validates entries field by field and drops invalid ones.
// Experts are sanitized first so teams can only reference experts from the
// same response.
func sanitizeCatalog(raw wireCatalog) Catalog {
	out := Catalog{Meta: Meta{Source: raw.Meta.Source, SyncedAt: raw.Meta.SyncedAt, Commit: raw.Meta.Commit}}
	records := raw.Items
	if len(records) > maxTemplates {
		records = records[:maxTemplates]
	}

	experts := make(map[string]Template)
	var items []Template
	for _, r := range records {
		if r.Kind != string(KindExpert) {
			continue
		}
		t, ok := sanitizeBase(r)
		if !ok {
			continue
		}
		instructions, ok := localized(r.Instructions, maxInstructionsLen)
		if !ok {
			continue
		}
		t.Instructions = instructions
		if r.Skills != nil {
			skills, ok := sanitizeSkills(r.Skills)
			if !ok {
				continue
			}
			t.Skills = skills
		}
		if _, dup := experts[t.ID]; dup {
			continue
		}
		experts[t.ID] = t
		items = append(items, t)
	}

	// Role display names may resolve to at most one expert per response;
	// ambiguous names disqualify teams that would rely on them.
	roleOwner := make(map[string]string)
	for id, t := range experts {
		for _, name := range []string{t.Name.Zh, t.Name.En} {
			if prev, seen := roleOwner[name]; seen && prev != id {
				roleOwner[name] = "" // ambiguous
				continue
			}
			roleOwner[name] = id
		}
	}

	teamNames := make(map[string]bool)
	for _, r := range records {
		if r.Kind != string(KindTeam) {
			continue
		}
		t, ok := sanitizeBase(r)
		if !ok {
			continue
		}
		roles, ok := localized(r.Roles, maxRolesLen)
		if !ok {
			continue
		}
		if experts[r.Leader].ID == "" || t.ID == r.Leader {
			continue
		}
		if len(r.Members) < 1 || len(r.Members) > maxMembers {
			continue
		}
		seen := map[string]bool{r.Leader: true}
		ambiguous := false
		for _, m := range r.Members {
			if experts[m].ID == "" || seen[m] {
				ok = false
				break
			}
			seen[m] = true
			for _, name := range []string{experts[m].Name.Zh, experts[m].Name.En} {
				if owner, has := roleOwner[name]; !has || owner == "" {
					ambiguous = true
				}
			}
		}
		if !ok || ambiguous || teamNames[t.Name.Zh] || teamNames[t.Name.En] {
			continue
		}
		teamNames[t.Name.Zh] = true
		teamNames[t.Name.En] = true
		t.Roles = roles
		t.Leader = r.Leader
		t.Members = append([]string(nil), r.Members...)
		items = append(items, t)
	}

	if items == nil {
		items = []Template{}
	}
	out.Items = items
	return out
}

// sanitizeBase validates the fields shared by expert and team entries.
func sanitizeBase(r wireItem) (Template, bool) {
	name, ok := localized(r.Name, maxNameLen)
	if !ok {
		return Template{}, false
	}
	description, ok := localized(r.Description, maxDescriptionLen)
	if !ok {
		return Template{}, false
	}
	if !idPattern.MatchString(r.ID) || !categories[r.Category] {
		return Template{}, false
	}
	icon := r.Icon
	if !icons[icon] {
		icon = "sparkle"
	}
	var tags []LocalizedText
	if len(r.Tags) > maxTags {
		r.Tags = r.Tags[:maxTags]
	}
	for _, tag := range r.Tags {
		if text, ok := localized(tag, maxTagLen); ok {
			tags = append(tags, text)
		}
	}
	return Template{
		ID:          r.ID,
		Kind:        Kind(r.Kind),
		Category:    r.Category,
		Icon:        icon,
		Name:        name,
		Description: description,
		Tags:        tags,
	}, true
}

// localized trims and validates a zh/en pair; both languages must be
// present and within limit.
func localized(raw wireLocalized, limit int) (LocalizedText, bool) {
	text := LocalizedText{Zh: strings.TrimSpace(raw.Zh), En: strings.TrimSpace(raw.En)}
	if text.Zh == "" || text.En == "" || len(text.Zh) > limit || len(text.En) > limit {
		return LocalizedText{}, false
	}
	return text, true
}

// sanitizeSkills validates the optional skill binding list.
func sanitizeSkills(skills []string) ([]string, bool) {
	if len(skills) > maxSkills {
		return nil, false
	}
	seen := make(map[string]bool, len(skills))
	out := make([]string, 0, len(skills))
	for _, s := range skills {
		if !idPattern.MatchString(s) || seen[s] {
			return nil, false
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, true
	}
	return out, true
}

// ─── Cached source ───────────────────────────────────────────────────────────

// SourceOption configures a Source (tests).
type SourceOption func(*Source)

// WithNow overrides the wall clock used for refresh scheduling (tests).
func WithNow(now func() time.Time) SourceOption {
	return func(s *Source) { s.now = now }
}

// Source caches the last good catalog and refreshes it on a fixed interval.
// Refresh never returns an error: a failure simply keeps the current list, so
// callers always have something to render.
type Source struct {
	client *Client
	now    func() time.Time

	mu          sync.Mutex
	templates   []Template
	meta        Meta
	lastAttempt time.Time
	fetchMu     sync.Mutex // held across check+fetch so concurrent refreshes collapse
}

// NewSource creates a cached catalog source around a client.
func NewSource(client *Client, opts ...SourceOption) *Source {
	s := &Source{
		client: client,
		now:    time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Current returns the last good catalog (possibly empty before the first
// successful refresh).
func (s *Source) Current() ([]Template, Meta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.templates, s.meta
}

// Refresh re-fetches the catalog if the cached copy is stale. Safe for
// concurrent use; concurrent refreshes collapse into one request, and a
// failure waits out the full interval before retrying.
func (s *Source) Refresh(ctx context.Context) {
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()

	now := s.now()
	s.mu.Lock()
	if now.Sub(s.lastAttempt) < refreshInterval {
		s.mu.Unlock()
		return
	}
	// Stamp before fetching so a failure waits out the full interval,
	// mirroring the reference client.
	s.lastAttempt = now
	s.mu.Unlock()

	catalog, err := s.client.Templates(ctx)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(catalog.Items) > 0 {
		s.templates = catalog.Items
		s.meta = catalog.Meta
	}
}
