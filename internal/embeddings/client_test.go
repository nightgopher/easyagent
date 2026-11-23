package embeddings

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbedValidatesAndReorders(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-key" {
			t.Error("missing authorization")
		}
		fmt.Fprint(w, `{"data":[{"index":1,"embedding":[3,4]},{"index":0,"embedding":[1,2]}]}`)
	}))
	defer server.Close()
	client := Client{Endpoint: server.URL, Model: "test", APIKey: "private-key", HTTPClient: server.Client()}
	vectors, err := client.Embed(context.Background(), []string{"first", "second"})
	if err != nil || len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][0] != 3 {
		t.Fatalf("unexpected result: %v %v", vectors, err)
	}
}

func TestEmbedRejectsRedirectAndMalformedResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://example.com", http.StatusTemporaryRedirect)
			return
		}
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]}`)
	}))
	defer server.Close()
	client := Client{Endpoint: server.URL + "/redirect", Model: "test", APIKey: "secret", HTTPClient: server.Client()}
	if _, err := client.Embed(context.Background(), []string{"a"}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected error: %v", err)
	}
	client.Endpoint = server.URL
	if _, err := client.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("duplicate vector index accepted")
	}
	client.Endpoint = "http://example.com/v1/embeddings"
	if _, err := client.Embed(context.Background(), []string{"a"}); err == nil {
		t.Fatal("insecure endpoint accepted")
	}
}
