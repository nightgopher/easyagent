package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestTerminalHandshakeErrorSurfacesServerReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"interactive terminal is not enabled on this host"}`))
	}))
	defer server.Close()
	conn, resp, err := websocket.DefaultDialer.DialContext(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected the handshake to fail")
	}
	if resp == nil {
		t.Fatal("expected the failure response alongside the handshake error")
	}
	text := terminalHandshakeError(resp).Error()
	for _, want := range []string{"HTTP 403", "interactive terminal is not enabled on this host", "EA_ENABLE_TERMINAL=1"} {
		if !strings.Contains(text, want) {
			t.Errorf("error %q does not mention %q", text, want)
		}
	}
}

func TestTerminalHandshakeErrorWithoutJSONBody(t *testing.T) {
	err := terminalHandshakeError(&http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("unexpected error: %v", err)
	}
}
