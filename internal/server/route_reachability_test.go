package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var routeWildcard = regexp.MustCompile(`\{[^}]*\}`)

// restRoutePatterns lists every literal route pattern the package registers on the REST mux, read
// from the source so a newly added route is covered without touching this test.
func restRoutePatterns(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var patterns []string
	for _, file := range files {
		// feishu_bridge.go serves its tool callback from its own listener, not the REST mux.
		if strings.HasSuffix(file, "_test.go") || file == "feishu_bridge.go" {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err)
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok || (recv.Name != "restMux" && recv.Name != "mux") {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				pattern, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				patterns = append(patterns, pattern)
			}
			return true
		})
	}
	return patterns
}

// Every route registered on the REST mux must also be forwarded by the top-level mux, which lists
// prefixes by hand. A missing one is not a 404: the web UI's catch-all answers with index.html.
func TestEveryRESTRouteIsReachableThroughTheTopLevelMux(t *testing.T) {
	top, ok := newSkillsServer(t, t.TempDir(), nil).Handler().(*http.ServeMux)
	require.True(t, ok, "Handler must return the top-level ServeMux")

	patterns := restRoutePatterns(t)
	require.Greater(t, len(patterns), 40, "route discovery found suspiciously few routes: %v", patterns)
	for _, pattern := range patterns {
		method, path, found := strings.Cut(pattern, " ")
		if !found {
			method, path = http.MethodGet, pattern
		}
		path = routeWildcard.ReplaceAllString(path, "x")
		_, matched := top.Handler(httptest.NewRequest(method, path, nil))
		assert.NotEqual(t, "/", matched, "%s falls through to the web UI catch-all; forward it in Handler()", pattern)
	}
}
