package ai

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/net/http/httpproxy"
)

// CodexProxyEnv is the environment variable through which a desktop client
// hands the system's proxy answer to the core for Codex/ChatGPT traffic:
// the OAuth token exchange (internal/codex) and the backend model requests
// (providers.CodexProvider). GUI-spawned processes have no shell
// environment, so the standard proxy variables are usually absent there;
// the desktop resolves them the way the user's browser does (mygo
// netproxy) and passes the result in this variable. Unrelated core traffic
// (gateways, Feishu, MCP) never reads it and stays unaffected.
const CodexProxyEnv = "EA_CODEX_PROXY"

// NamedProxy resolves the proxy from the named environment variable first,
// then the standard proxy variables (HTTP_PROXY/HTTPS_PROXY/NO_PROXY), and
// finally direct. An unusable named value falls back to the standard
// variables rather than pinning traffic to a broken proxy.
//
// The standard variables are read per request, not once per process as
// http.ProxyFromEnvironment does: a long-running core picks up changes
// without a restart, and tests in one process do not freeze each other's
// environment.
func NamedProxy(name string) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			if u, err := url.Parse(v); err == nil && u.Host != "" {
				return u, nil
			}
		}
		return httpproxy.FromEnvironment().ProxyFunc()(req.URL)
	}
}
