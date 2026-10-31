package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type searchTools struct{ catalog *toolCatalog }

func (*searchTools) Name() string { return SearchToolsName }
func (*searchTools) Description() string {
	return "Search additional tools by capability, keyword or exact tool name. Matching full definitions are loaded for the next request; then call the original tool directly. Empty query browses sources (or tools within source) without loading. Search locally; this never executes a business tool."
}
func (*searchTools) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"query":  map[string]any{"type": "string", "maxLength": 512, "description": "Capability, keywords, or exact tool name. Empty to browse."},
		"source": map[string]any{"type": "string", "description": "Optional source ID returned by browsing."},
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 8, "description": "Number of definitions to load; default 5. Ignored when browsing."},
		"cursor": map[string]any{"type": "string", "description": "Next-page cursor from browsing; reuse the same source and empty query."},
	}, "required": []string{"query"}, "additionalProperties": false}
}

type searchParams struct {
	Query  *string `json:"query"`
	Source string  `json:"source,omitempty"`
	Limit  *int    `json:"limit,omitempty"`
	Cursor string  `json:"cursor,omitempty"`
}

func parseSearch(raw json.RawMessage) (searchParams, error) {
	var p searchParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	if p.Query == nil || utf8.RuneCountInString(*p.Query) > 512 {
		return p, fmt.Errorf("query is required and must contain at most 512 characters")
	}
	query := strings.TrimSpace(*p.Query)
	p.Query = &query
	if p.Limit != nil && (*p.Limit < 1 || *p.Limit > 8) {
		return p, fmt.Errorf("limit must be between 1 and 8")
	}
	if query != "" && p.Cursor != "" {
		return p, fmt.Errorf("cursor is only valid for an empty browsing query")
	}
	return p, nil
}
func (*searchTools) Validate(raw json.RawMessage) (json.RawMessage, error) {
	p, err := parseSearch(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(p)
}
func (*searchTools) IsConcurrencySafe(json.RawMessage) bool { return true }

type toolSource struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Count       int    `json:"count"`
	Description string `json:"description"`
}

func (c *toolCatalog) sources(filter string) []toolSource {
	byID := map[string]*toolSource{}
	for _, name := range c.names {
		e := c.entries[name]
		if !e.info.Deferred || filter != "" && e.info.SourceID != filter {
			continue
		}
		s := byID[e.info.SourceID]
		if s == nil {
			s = &toolSource{ID: e.info.SourceID, Label: e.info.SourceLabel}
			byID[s.ID] = s
		}
		s.Count++
		if s.Count <= 2 {
			s.Description = shortText(strings.TrimSpace(s.Description+" "+name+": "+e.definition.Description), 180)
		}
	}
	var sources []toolSource
	for _, s := range byID {
		sources = append(sources, *s)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	return sources
}
func shortText(text string, max int) string {
	r := []rune(strings.Join(strings.Fields(text), " "))
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return string(r)
}

// searchWords handles camelCase, snake_case, and Chinese bigrams without a
// network service. This is keyword retrieval, not semantic embedding search.
func searchWords(text string) []string {
	var b strings.Builder
	var previous rune
	for _, r := range text {
		if unicode.IsUpper(r) && (unicode.IsLower(previous) || unicode.IsDigit(previous)) {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(r))
		previous = r
	}
	parts := strings.FieldsFunc(b.String(), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	seen := map[string]bool{}
	var words []string
	add := func(w string) {
		if w != "" && !seen[w] {
			seen[w] = true
			words = append(words, w)
		}
	}
	for _, p := range parts {
		add(p)
		runes := []rune(p)
		for i := 0; i+1 < len(runes); i++ {
			if unicode.Is(unicode.Han, runes[i]) && unicode.Is(unicode.Han, runes[i+1]) {
				add(string(runes[i : i+2]))
			}
		}
	}
	return words
}
func scoreTool(e catalogEntry, query string) int {
	if strings.EqualFold(e.definition.Name, query) {
		return 1000000
	}
	for _, alias := range e.info.Aliases {
		if strings.EqualFold(alias, query) {
			return 900000
		}
	}
	fields := []struct {
		text   string
		weight int
	}{
		{e.definition.Name + " " + strings.Join(e.info.Aliases, " "), 100},
		{strings.Join(e.info.SearchTerms, " "), 30},
		{e.info.SourceID + " " + e.info.SourceLabel, 10},
		{e.definition.Description, 5},
	}
	score := 0
	words := searchWords(query)
	for _, field := range fields {
		text := strings.ToLower(field.text)
		for _, word := range words {
			if strings.Contains(text, word) {
				score += field.weight
			}
		}
	}
	return score
}

func (s *searchTools) Execute(ctx context.Context, raw json.RawMessage, _ func(PartialResult)) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	p, err := parseSearch(raw)
	if err != nil {
		return ToolResult{}, err
	}
	if *p.Query == "" {
		return s.browse(p)
	}
	limit := 5
	if p.Limit != nil {
		limit = *p.Limit
	}
	type match struct {
		name  string
		score int
	}
	var matches []match
	for _, name := range s.catalog.names {
		e := s.catalog.entries[name]
		if p.Source != "" && e.info.SourceID != p.Source {
			continue
		}
		if !e.info.Deferred {
			if strings.EqualFold(name, *p.Query) {
				return ToolResult{Content: name + " is already available; call it directly."}, nil
			}
			continue
		}
		if score := scoreTool(e, *p.Query); score > 0 {
			matches = append(matches, match{name, score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].name < matches[j].name
	})
	// An exact canonical name/alias is a lookup, not a broad namespace query.
	if len(matches) > 0 && matches[0].score >= 900000 {
		end := 1
		for end < len(matches) && matches[end].score == matches[0].score {
			end++
		}
		matches = matches[:end]
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	if len(matches) == 0 {
		return ToolResult{Content: "No matching tools. Try different keywords, an exact name, or an empty query to browse sources. Unavailable or disabled tools are not searchable."}, nil
	}
	record := discoveryRecord{Kind: "tool_discovery", Version: 1, CatalogRevision: s.catalog.revision}
	var b strings.Builder
	b.WriteString("These tools will be available in the next request. Call the original names directly:\n")
	for _, m := range matches {
		e := s.catalog.entries[m.name]
		fmt.Fprintf(&b, "- %s [%s]: %s\n", m.name, e.info.SourceID, shortText(e.definition.Description, 280))
		for _, guideline := range e.guidelines {
			fmt.Fprintf(&b, "  Usage: %s\n", guideline)
		}
		record.Selected = append(record.Selected, discoveredTool{Name: m.name, DefinitionHash: e.hash})
	}
	return ToolResult{Content: b.String(), Details: record}, nil
}

type browseCursor struct {
	Revision, Source string
	Offset           int
}

func (s *searchTools) browse(p searchParams) (ToolResult, error) {
	offset := 0
	if p.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(p.Cursor)
		var cursor browseCursor
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Revision != s.catalog.revision || cursor.Source != p.Source || cursor.Offset < 0 {
			return ToolResult{}, fmt.Errorf("invalid or expired browse cursor; restart with an empty query")
		}
		offset = cursor.Offset
	}
	var rows []string
	if p.Source == "" {
		for _, source := range s.catalog.sources("") {
			rows = append(rows, fmt.Sprintf("%s (%s): %d tools — %s", source.ID, source.Label, source.Count, source.Description))
		}
	} else {
		for _, name := range s.catalog.names {
			e := s.catalog.entries[name]
			if e.info.Deferred && e.info.SourceID == p.Source {
				rows = append(rows, name+": "+shortText(e.definition.Description, 180))
			}
		}
	}
	if offset > len(rows) {
		return ToolResult{}, fmt.Errorf("invalid browse cursor offset")
	}
	end := min(offset+20, len(rows))
	content := "Browsing only; no definitions loaded. Search an exact tool name to load it.\n" + strings.Join(rows[offset:end], "\n")
	if end < len(rows) {
		data, _ := json.Marshal(browseCursor{Revision: s.catalog.revision, Source: p.Source, Offset: end})
		content += "\nNext cursor: " + base64.RawURLEncoding.EncodeToString(data)
	}
	return ToolResult{Content: content}, nil
}
