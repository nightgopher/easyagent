package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nightgopher/easyagent/sdk/agent"
)

// ─── SearXNG-based Web Search ──────────────────────────────────────────────
//
// Uses a self-hostable SearXNG instance as the search backend (no API key
// required, unlike Tavily/Brave). Without an explicit SearXNG endpoint, use
// DuckDuckGo with Bing RSS as a fallback when the first provider is blocked.
//
// Env vars:
//   SEARXNG_URL  — base URL of the SearXNG instance (e.g. http://localhost:8080)
//   WEB_SEARCH_TIMEOUT — per-request timeout in seconds (default: 15)

// WebSearchTool searches the web and returns summarized results.
type WebSearchTool struct {
	timeout time.Duration
	client  *http.Client
	engine  searchEngine
}

type searchEngine interface {
	search(ctx context.Context, query string, maxResults int) ([]SearchResult, error)
}

// SearchResult represents a single search result item.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// WebSearchParams 工具参数。
type WebSearchParams struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

// NewWebSearchTool creates a WebSearchTool. It auto-detects the search engine
// from environment variables.
func NewWebSearchTool() *WebSearchTool {
	timeoutSec := 15
	if v := os.Getenv("WEB_SEARCH_TIMEOUT"); v != "" {
		if n := parseIntDefault(v, 15); n > 0 {
			timeoutSec = n
		}
	}

	timeout := time.Duration(timeoutSec) * time.Second

	var engine searchEngine
	if searxngURL := strings.TrimSpace(os.Getenv("SEARXNG_URL")); searxngURL != "" {
		engine = newSearXNGEngine(searxngURL, timeout)
	} else {
		engine = &fallbackSearchEngine{
			primary:        newDuckDuckGoEngine(timeout),
			fallback:       newBingEngine(timeout),
			primaryTimeout: timeout / 2,
		}
	}

	return &WebSearchTool{
		timeout: timeout,
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				if isPrivateHost(req.URL.Hostname()) {
					return fmt.Errorf("redirect to private host blocked: %s", req.URL.Hostname())
				}
				return nil
			},
		},
		engine: engine,
	}
}

func (t *WebSearchTool) Name() string { return "web_search" }
func (t *WebSearchTool) Description() string {
	return "Search the web for information. Returns titles, URLs, and snippets. " +
		"Useful for finding documentation, error solutions, API references, or " +
		"library usage examples. Use web_fetch to read the full content of a result."
}
func (t *WebSearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The search query.",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results to return (default: 8, max: 20).",
			},
		},
		"required": []string{"query"},
	}
}

func (t *WebSearchTool) Validate(raw json.RawMessage) (json.RawMessage, error) {
	var params WebSearchParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if params.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if params.MaxResults <= 0 {
		params.MaxResults = 8
	}
	if params.MaxResults > 20 {
		params.MaxResults = 20
	}
	return json.Marshal(params)
}

func (t *WebSearchTool) Execute(ctx context.Context, raw json.RawMessage, _ func(agent.PartialResult)) (agent.ToolResult, error) {
	var params WebSearchParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return agent.ToolResult{IsError: true}, err
	}

	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	results, err := t.engine.search(ctx, params.Query, params.MaxResults)
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("search failed: %v", err),
			IsError: true,
		}, nil
	}

	if len(results) == 0 {
		return agent.ToolResult{
			Content: fmt.Sprintf("No results found for: %s", params.Query),
		}, nil
	}

	// Format results as readable text
	var b strings.Builder
	fmt.Fprintf(&b, "🔍 Search results for: %s\n\n", params.Query)
	for i, r := range results {
		fmt.Fprintf(&b, "%d. **%s**\n", i+1, r.Title)
		fmt.Fprintf(&b, "   URL: %s\n", r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
		b.WriteString("\n")
	}
	b.WriteString("Use web_fetch to read the full content of any result.\n")

	return agent.ToolResult{Content: b.String()}, nil
}

// IsConcurrencySafe 允许 web_search 进入并行批次和 readonly 子代理工具集：
// 结构体字段与搜索引擎实现（baseURL + http.Client）构造后只读，无共享可变状态。
func (t *WebSearchTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

// Explicitly configured search servers are never passed to this fallback: a
// private query must not silently leave the configured search service.
type fallbackSearchEngine struct {
	primary        searchEngine
	fallback       searchEngine
	primaryTimeout time.Duration
}

func (e *fallbackSearchEngine) search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	primaryCtx, cancel := context.WithTimeout(ctx, e.primaryTimeout)
	results, primaryErr := e.primary.search(primaryCtx, query, maxResults)
	cancel()
	if primaryErr == nil {
		return results, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	results, fallbackErr := e.fallback.search(ctx, query, maxResults)
	if fallbackErr == nil {
		return results, nil
	}
	return nil, fmt.Errorf("all search providers failed: %w", errors.Join(primaryErr, fallbackErr))
}

// ─── Bing RSS Engine (fallback, no API key) ────────────────────────────────

type bingEngine struct {
	baseURL string
	client  *http.Client
}

func newBingEngine(timeout time.Duration) *bingEngine {
	return &bingEngine{baseURL: "https://www.bing.com/search", client: &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if isPrivateHost(req.URL.Hostname()) {
				return fmt.Errorf("redirect to private host blocked")
			}
			return nil
		},
	}}
}

func (e *bingEngine) search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	u, err := url.Parse(e.baseURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "rss")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; EasyAgent/1.0)")
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bing request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bing returned HTTP %d", resp.StatusCode)
	}
	var feed struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				Description string `xml:"description"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, maxHTTPContentLength)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("parse bing RSS: %w", err)
	}
	results := make([]SearchResult, 0, min(maxResults, len(feed.Channel.Items)))
	for _, item := range feed.Channel.Items {
		link, err := url.Parse(strings.TrimSpace(item.Link))
		if err != nil || link.Hostname() == "" || link.User != nil || (link.Scheme != "http" && link.Scheme != "https") {
			continue
		}
		title := strings.TrimSpace(cleanHTMLText(item.Title))
		if title == "" {
			continue
		}
		results = append(results, SearchResult{Title: title, URL: link.String(), Snippet: truncateSnippet(cleanHTMLText(item.Description), 200)})
		if len(results) >= maxResults {
			break
		}
	}
	return results, nil
}

// ─── SearXNG Engine ────────────────────────────────────────────────────────

type searxngEngine struct {
	baseURL string
	client  *http.Client
}

func newSearXNGEngine(baseURL string, timeout time.Duration) *searxngEngine {
	return &searxngEngine{
		baseURL: strings.TrimRight(baseURL, "/"),
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				if isPrivateHost(req.URL.Hostname()) {
					return fmt.Errorf("redirect to private host blocked")
				}
				return nil
			},
		},
	}
}

func (e *searxngEngine) search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	// SearXNG JSON API: GET /search?q=<query>&format=json
	u := fmt.Sprintf("%s/search?q=%s&format=json", e.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "easyagent-agent/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searxng returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPContentLength))
	if err != nil {
		return nil, fmt.Errorf("read searxng response: %w", err)
	}

	// SearXNG JSON format: { "results": [ { "title":..., "url":..., "content":... } ] }
	var srx struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &srx); err != nil {
		return nil, fmt.Errorf("parse searxng JSON: %w", err)
	}

	results := make([]SearchResult, 0, maxResults)
	for i, r := range srx.Results {
		if i >= maxResults {
			break
		}
		results = append(results, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: truncateSnippet(r.Content, 200),
		})
	}
	return results, nil
}

// ─── DuckDuckGo HTML Engine (fallback, no API key) ─────────────────────────

type duckduckgoEngine struct {
	client *http.Client
}

func newDuckDuckGoEngine(timeout time.Duration) *duckduckgoEngine {
	return &duckduckgoEngine{
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				if isPrivateHost(req.URL.Hostname()) {
					return fmt.Errorf("redirect to private host blocked")
				}
				return nil
			},
		},
	}
}

func (e *duckduckgoEngine) search(ctx context.Context, query string, maxResults int) ([]SearchResult, error) {
	// DuckDuckGo HTML endpoint
	u := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "easyagent-agent/1.0")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPContentLength))
	if err != nil {
		return nil, fmt.Errorf("read duckduckgo response: %w", err)
	}
	lower := strings.ToLower(string(body))
	if strings.Contains(lower, "anomaly-modal") || strings.Contains(lower, `id="challenge-form"`) {
		return nil, fmt.Errorf("duckduckgo requested human verification")
	}

	// Parse DuckDuckGo HTML results
	results := parseDuckDuckGoHTML(string(body), maxResults)
	if len(results) == 0 && !strings.Contains(lower, "no results") && !strings.Contains(lower, "no-results") {
		return nil, fmt.Errorf("duckduckgo returned an unrecognized results page")
	}
	return results, nil
}

// parseDuckDuckGoHTML extracts search results from DuckDuckGo HTML response.
// DDG HTML format: results in <a class="result__a" href="...">title</a>
// snippets in <a class="result__snippet">...</a>
func parseDuckDuckGoHTML(html string, maxResults int) []SearchResult {
	var results []SearchResult

	// Simple HTML extraction without external deps.
	// Result links: class="result__a"
	linkPattern := `class="result__a"`
	snippetPattern := `class="result__snippet"`

	// Split by result blocks
	linkParts := strings.Split(html, linkPattern)
	for i := 1; i < len(linkParts) && len(results) < maxResults; i++ {
		part := linkParts[i]

		// Extract href
		href := extractAttribute(part, "href=")
		if href == "" {
			continue
		}
		// DDG wraps links in a redirect URL; extract the actual URL
		actualURL := extractDDGRedirectURL(href)
		if actualURL == "" {
			continue
		}

		// Extract title text
		title := extractTextBetween(part, ">", "</a>")
		title = cleanHTMLText(title)
		if title == "" {
			continue
		}

		// Look for snippet in the same block
		snippet := ""
		if snippetIdx := strings.Index(part, snippetPattern); snippetIdx >= 0 {
			snippetPart := part[snippetIdx:]
			snippet = extractTextBetween(snippetPart, ">", "</a>")
			snippet = cleanHTMLText(snippet)
		}

		results = append(results, SearchResult{
			Title:   title,
			URL:     actualURL,
			Snippet: truncateSnippet(snippet, 200),
		})
	}

	return results
}

// extractAttribute extracts an attribute value from HTML fragment like href="..."
func extractAttribute(s string, attr string) string {
	idx := strings.Index(s, attr)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(attr):]
	// Skip quote char
	if len(rest) == 0 {
		return ""
	}
	quote := rest[0]
	rest = rest[1:]
	end := strings.IndexByte(rest, quote)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// extractTextBetween extracts text between two delimiters.
func extractTextBetween(s string, start string, end string) string {
	startIdx := strings.Index(s, start)
	if startIdx < 0 {
		return ""
	}
	rest := s[startIdx+len(start):]
	endIdx := strings.Index(rest, end)
	if endIdx < 0 {
		return ""
	}
	return rest[:endIdx]
}

// extractDDGRedirectURL extracts the actual URL from DDG's redirect URL format.
// DDG links look like: //duckduckgo.com/l/?uddg=<actual_url>&rut=...
func extractDDGRedirectURL(href string) string {
	// Remove leading // if present
	href = strings.TrimPrefix(href, "//")

	if strings.HasPrefix(href, "duckduckgo.com/l/") {
		// Parse query params
		if uq := extractQueryParam(href, "uddg"); uq != "" {
			decoded, err := url.QueryUnescape(uq)
			if err == nil {
				return decoded
			}
			return uq
		}
	}
	// Direct URL: must start with http:// or https:// after stripping prefix
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	return ""
}

// extractQueryParam extracts a URL query parameter value.
func extractQueryParam(rawURL string, key string) string {
	u, err := url.Parse("https://" + rawURL)
	if err != nil {
		return ""
	}
	return u.Query().Get(key)
}

// cleanHTMLText strips basic HTML tags from text.
func cleanHTMLText(s string) string {
	// Remove HTML tags
	for {
		start := strings.Index(s, "<")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], ">")
		if end < 0 {
			break
		}
		s = s[:start] + s[start+end+1:]
	}
	s = strings.TrimSpace(s)
	// Unescape common HTML entities
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	return s
}

func truncateSnippet(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func parseIntDefault(s string, def int) int {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}
