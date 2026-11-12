package ai

import (
	"net/http"
	"net/url"
	"testing"
)

func TestNamedProxyPrefersNamedVariable(t *testing.T) {
	t.Setenv(CodexProxyEnv, "http://127.0.0.1:7890")
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:1")
	proxy := NamedProxy(CodexProxyEnv)
	u, err := proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "auth.openai.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != "127.0.0.1:7890" {
		t.Fatalf("want named proxy, got %v", u)
	}
}

func TestNamedProxyFallsBackToStandardEnv(t *testing.T) {
	t.Setenv(CodexProxyEnv, "")
	t.Setenv("HTTPS_PROXY", "http://10.0.0.2:2")
	proxy := NamedProxy(CodexProxyEnv)
	u, err := proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "auth.openai.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != "10.0.0.2:2" {
		t.Fatalf("want standard env proxy, got %v", u)
	}
}

func TestNamedProxyUnusableValueAndNoEnvMeansDirect(t *testing.T) {
	t.Setenv(CodexProxyEnv, "not-a-proxy")
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	proxy := NamedProxy(CodexProxyEnv)
	u, err := proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "auth.openai.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if u != nil {
		t.Fatalf("want direct, got %v", u)
	}
}
