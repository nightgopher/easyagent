package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"github.com/gorilla/websocket"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelayPairAndPinnedTransport(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "fixture.json")
	cmd := exec.Command("go", "run", "./internal/relay/testdata/interop", out)
	cmd.Dir = ".."
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); _ = cmd.Wait() }()
	var data []byte
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		data, err = os.ReadFile(out)
		if err == nil && len(data) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatal("relay fixture did not start")
	}
	var fixture struct {
		Code        json.RawMessage `json:"code"`
		Certificate string          `json:"outer_certificate"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	code, err := parseRelayPair(string(fixture.Code), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certDER, err := base64.StdEncoding.DecodeString(fixture.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	outer := &websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	b, err := newRelayBackend(code.relayConfig, "", outer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var paired struct {
		Token string `json:"device_token"`
	}
	if err = b.request(ctx, "POST", "/pair/claim", map[string]string{"pair_token": code.PairToken, "name": "test phone"}, &paired); err != nil {
		t.Fatal(err)
	}
	if !relaySecretPattern.MatchString(paired.Token) {
		t.Fatal("invalid device token")
	}
	b.Token = paired.Token
	ws, response, err := b.socketDialer.DialContext(ctx, strings.Replace(b.URL, "https://", "wss://", 1)+"/ws", map[string][]string{"Authorization": {"Bearer " + paired.Token}})
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = ws.WriteMessage(websocket.TextMessage, []byte("private test")); err != nil {
		t.Fatal(err)
	}
	_, echoed, err := ws.ReadMessage()
	if err != nil || string(echoed) != "private test" {
		t.Fatalf("inner websocket: %q %v", echoed, err)
	}
	_ = ws.Close()
	var sessions []map[string]any
	if err = b.request(ctx, "GET", "/sessions", nil, &sessions); err != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %v %v", sessions, err)
	}
	wrong := code.relayConfig
	wrong.Fingerprint = "sha256:" + base64.StdEncoding.EncodeToString(make([]byte, 32))
	bad, err := newRelayBackend(wrong, "", outer)
	if err != nil {
		t.Fatal(err)
	}
	if err = bad.request(ctx, "GET", "/health", nil, nil); err == nil {
		t.Fatal("wrong host identity accepted")
	}
	expired := strings.Replace(string(fixture.Code), code.ExpiresAt.Format(time.RFC3339Nano), time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), 1)
	if _, err = parseRelayPair(expired, time.Now()); err == nil {
		t.Fatal("expired code accepted")
	}
}
