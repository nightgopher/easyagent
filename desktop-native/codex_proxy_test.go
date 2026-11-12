package main

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func contextCanceled() error { return context.Canceled }

func TestCodexProxyURLFollowsLookup(t *testing.T) {
	orig := codexProxyLookup
	defer func() { codexProxyLookup = orig }()
	codexProxyLookup = func(*http.Request) (*url.URL, error) {
		return &url.URL{Scheme: "http", Host: "127.0.0.1:7890"}, nil
	}
	if got := codexProxyURL(); got != "http://127.0.0.1:7890" {
		t.Fatalf("got %q", got)
	}
	codexProxyLookup = func(*http.Request) (*url.URL, error) { return nil, nil }
	if got := codexProxyURL(); got != "" {
		t.Fatalf("want empty without proxy, got %q", got)
	}
}

func TestCodexProxyURLSwallowsLookupErrors(t *testing.T) {
	orig := codexProxyLookup
	defer func() { codexProxyLookup = orig }()
	codexProxyLookup = func(*http.Request) (*url.URL, error) {
		return nil, contextCanceled()
	}
	if got := codexProxyURL(); got != "" {
		t.Fatalf("want empty on error, got %q", got)
	}
}
