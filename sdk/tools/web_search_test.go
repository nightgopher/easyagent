package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type stubSearchEngine struct {
	results []SearchResult
	err     error
	calls   int
}

type searchRoundTrip func(*http.Request) (*http.Response, error)

func (f searchRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDuckDuckGoDistinguishesResultsAndBlockedPages(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantCount  int
		wantErr    bool
	}{
		{"ordinary result mentioning captcha", `<a class="result__a" href="https://example.com">CAPTCHA docs</a>`, 1, false},
		{"legitimate empty result", `<div>No results found for query</div>`, 0, false},
		{"challenge", `<div class="anomaly-modal">Verify</div>`, 0, true},
		{"unexpected markup", `<html>unrecognized page</html>`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := newDuckDuckGoEngine(time.Second)
			engine.client = &http.Client{Transport: searchRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			results, err := engine.search(context.Background(), "query", 5)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Len(t, results, tc.wantCount)
			}
		})
	}
}

func (s *stubSearchEngine) search(_ context.Context, _ string, _ int) ([]SearchResult, error) {
	s.calls++
	return s.results, s.err
}

func TestWebSearchFallbackAndPrivateServiceIsolation(t *testing.T) {
	primary := &stubSearchEngine{err: errors.New("provider unavailable")}
	secondary := &stubSearchEngine{results: []SearchResult{{Title: "Result", URL: "https://example.com"}}}
	engine := &fallbackSearchEngine{primary: primary, fallback: secondary, primaryTimeout: time.Second}
	results, err := engine.search(context.Background(), "query", 5)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 1, primary.calls)
	require.Equal(t, 1, secondary.calls)

	primary.err = nil
	primary.results = nil // A valid empty response is not a provider failure.
	_, err = engine.search(context.Background(), "no results", 5)
	require.NoError(t, err)
	require.Equal(t, 1, secondary.calls)

	t.Setenv("SEARXNG_URL", "http://127.0.0.1:12345")
	_, explicitOnly := NewWebSearchTool().engine.(*searxngEngine)
	require.True(t, explicitOnly, "configured searches must not fall back to a third party")
}

func TestBingRSSResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "golang docs", r.URL.Query().Get("q"))
		require.Equal(t, "rss", r.URL.Query().Get("format"))
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss><channel>
			<item><title>Go &amp; Docs</title><link>https://go.dev/doc/</link><description>Official documentation</description></item>
			<item><title>Unsafe</title><link>javascript:alert(1)</link></item>
			<item><title>Second</title><link>https://example.com/</link><description>More</description></item>
		</channel></rss>`))
	}))
	defer server.Close()
	engine := &bingEngine{baseURL: server.URL, client: server.Client()}
	results, err := engine.search(context.Background(), "golang docs", 2)
	require.NoError(t, err)
	require.Equal(t, []SearchResult{
		{Title: "Go & Docs", URL: "https://go.dev/doc/", Snippet: "Official documentation"},
		{Title: "Second", URL: "https://example.com/", Snippet: "More"},
	}, results)
}

func TestBingRejectsNonRSS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>verification required</html>`))
	}))
	defer server.Close()
	engine := &bingEngine{baseURL: server.URL, client: server.Client()}
	_, err := engine.search(context.Background(), "query", 5)
	require.Error(t, err)
}

func TestWebSearchValidate(t *testing.T) {
	tool := NewWebSearchTool()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "valid query",
			input:   `{"query": "golang context tutorial"}`,
			wantErr: false,
		},
		{
			name:    "empty query",
			input:   `{"query": ""}`,
			wantErr: true,
		},
		{
			name:    "missing query",
			input:   `{"max_results": 5}`,
			wantErr: true,
		},
		{
			name:    "with max_results",
			input:   `{"query": "test", "max_results": 5}`,
			wantErr: false,
		},
		{
			name:    "max_results too large gets clamped",
			input:   `{"query": "test", "max_results": 100}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validated, err := tool.Validate(json.RawMessage(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				// Verify the validated params are sensible
				var params WebSearchParams
				require.NoError(t, json.Unmarshal(validated, &params))
				if params.MaxResults > 20 {
					t.Errorf("MaxResults should be clamped to 20, got %d", params.MaxResults)
				}
			}
		})
	}
}

func TestWebSearchSearXNG(t *testing.T) {
	// Mock SearXNG server
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			http.Error(w, "missing query", http.StatusBadRequest)
			return
		}

		response := map[string]any{
			"results": []map[string]any{
				{"title": "Go Context Tutorial", "url": "https://golang.org/doc/context", "content": "Learn how to use context in Go."},
				{"title": "Advanced Go Patterns", "url": "https://example.com/advanced", "content": "Deep dive into Go concurrency."},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	engine := newSearXNGEngine(server.URL, 0)
	// Override client to skip real network checks
	engine.client = server.Client()

	results, err := engine.search(context.Background(), "golang context", 5)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	if results[0].Title != "Go Context Tutorial" {
		t.Errorf("First result title = %q, want %q", results[0].Title, "Go Context Tutorial")
	}
	if results[0].URL != "https://golang.org/doc/context" {
		t.Errorf("First result URL = %q", results[0].URL)
	}
}

func TestWebSearchFormatOutput(t *testing.T) {
	// Test with mock SearXNG
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"results": []map[string]any{
				{"title": "Test Result", "url": "https://example.com", "content": "This is a snippet."},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	t.Setenv("SEARXNG_URL", server.URL)
	tool := NewWebSearchTool()

	validated, _ := tool.Validate(json.RawMessage(`{"query": "test"}`))
	result, err := tool.Execute(context.Background(), validated, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.IsError {
		t.Error("Expected non-error result")
	}

	if result.Content == "" {
		t.Error("Expected non-empty content")
	}
}

func TestParseDuckDuckGoHTML(t *testing.T) {
	html := `<html><body>
	<div class="result">
		<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgolang.org%2F&amp;rut=abc">Go Programming Language</a>
		<a class="result__snippet" href="#">A statically typed, compiled programming language</a>
	</div>
	<div class="result">
		<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fgo&amp;rut=def">Learn Go</a>
		<a class="result__snippet" href="#">Go tutorial for beginners</a>
	</div>
	</body></html>`

	results := parseDuckDuckGoHTML(html, 5)

	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}

	if results[0].Title != "Go Programming Language" {
		t.Errorf("Title[0] = %q, want %q", results[0].Title, "Go Programming Language")
	}
	if results[0].URL != "https://golang.org/" {
		t.Errorf("URL[0] = %q, want %q", results[0].URL, "https://golang.org/")
	}
	if results[1].Title != "Learn Go" {
		t.Errorf("Title[1] = %q, want %q", results[1].Title, "Learn Go")
	}
}

func TestExtractDDGRedirectURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "//duckduckgo.com/l/?uddg=https%3A%2F%2Fgolang.org%2F&rut=abc",
			expected: "https://golang.org/",
		},
		{
			input:    "https://example.com/direct",
			expected: "https://example.com/direct",
		},
		{
			input:    "duckduckgo.com/l/?uddg=https%3A%2F%2Fpkg.go.dev",
			expected: "https://pkg.go.dev",
		},
		{
			input:    "invalid",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input[:min(len(tt.input), 20)], func(t *testing.T) {
			got := extractDDGRedirectURL(tt.input)
			if got != tt.expected {
				t.Errorf("extractDDGRedirectURL(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCleanHTMLText(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"<b>hello</b>", "hello"},
		{"plain text", "plain text"},
		{"a &amp; b", "a & b"},
		{"&lt;tag&gt;", "<tag>"},
		{"", ""},
	}

	for _, tt := range tests {
		got := cleanHTMLText(tt.input)
		if got != tt.expected {
			t.Errorf("cleanHTMLText(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestTruncateSnippet(t *testing.T) {
	short := "hello"
	if got := truncateSnippet(short, 10); got != "hello" {
		t.Errorf("Expected %q, got %q", "hello", got)
	}

	long := "This is a very long snippet that should be truncated to fit within the specified limit."
	got := truncateSnippet(long, 80)
	// We check it was truncated (shorter than original + ends with ...)
	if len(got) >= len(long) {
		t.Errorf("Expected truncation, got full length: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("Expected truncation suffix '...', got %q", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
