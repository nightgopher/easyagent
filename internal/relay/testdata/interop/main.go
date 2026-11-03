// Local integration fixture for the real Android and Apple networking stacks.
// Never used by production: all identities and data are temporary test fixtures.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nightgopher/easyagent/internal/relay"
)

func main() {
	if len(os.Args) != 2 {
		panic("private fixture output path required")
	}
	dir, err := os.MkdirTemp("", "easyagent-relay-interop-")
	check(err)
	defer os.RemoveAll(dir)
	keyBytes := make([]byte, 32)
	_, err = rand.Read(keyBytes)
	check(err)
	key := base64.RawURLEncoding.EncodeToString(keyBytes)
	cfg := relay.BrokerConfig{StatePath: filepath.Join(dir, "broker.json"), RegistrationKey: key}
	b, err := relay.NewBroker(cfg)
	check(err)
	var broker atomic.Pointer[relay.Broker]
	broker.Store(b)
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { broker.Load().Handler().ServeHTTP(w, r) }))
	defer s.Close()
	defer func() { _ = broker.Load().Close() }()
	base := os.Getenv("EA_RELAY_INTEROP_URL")
	public := base != ""
	if !public {
		base = s.URL
		// Trust only this ephemeral outer certificate, within the fixture process.
		http.DefaultTransport = s.Client().Transport
		websocket.DefaultDialer.TLSClientConfig = s.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	} else {
		key = os.Getenv("EA_RELAY_REGISTRATION_KEY")
		if len(key) < 32 {
			panic("public fixture requires an explicit registration key")
		}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if relay.DeviceID(r.Context()) == "" {
			http.Error(w, "unauthorized", 401)
			return
		}
		switch r.URL.Path {
		case "/ws":
			ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer ws.Close()
			for {
				kind, data, err := ws.ReadMessage()
				if err != nil {
					return
				}
				if ws.WriteMessage(kind, data) != nil {
					return
				}
			}
		case "/echo":
			_, _ = io.Copy(w, http.MaxBytesReader(w, r.Body, 4<<20))
		case "/attachment":
			_, _ = w.Write(bytes.Repeat([]byte("attachment-content\n"), 65536))
		case "/sessions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"fixture","title":"saved conversation","last_active":1,"created_at":1,"pinned":false,"archived":false}]`))
		case "/sessions/fixture/run":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"type":"snapshot","session_id":"fixture","seq":0,"messages":[{"role":"assistant","content":"Saved private chat over relay"}],"events":[],"pending_confirmations":[]}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sessions":[],"ok":true}`))
		}
	})
	host, err := relay.NewHost(filepath.Join(dir, "host.json"), handler)
	check(err)
	defer host.Close()
	check(host.Configure(context.Background(), true, base, key))
	deadline := time.Now().Add(10 * time.Second)
	for !host.Status().Online && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	code, err := host.NewPair("Fixture computer")
	check(err)
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "denied", 401)
			return
		}
		switch r.URL.Path {
		case "/revoke":
			for _, device := range host.Status().Devices {
				if err := host.Revoke(device.ID); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
			}
		case "/restart":
			if public {
				_ = host.Close()
				host.Start()
				break
			}
			next, err := relay.NewBroker(cfg)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			old := broker.Swap(next)
			_ = old.Close()
		case "/disable":
			if err := host.Configure(r.Context(), false, "", ""); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		case "/pair":
			code, err := host.NewPair("Fixture computer")
			if err != nil {
				http.Error(w, err.Error(), 409)
				return
			}
			_ = json.NewEncoder(w).Encode(code)
			return
		}
		_ = json.NewEncoder(w).Encode(host.Status())
	}))
	defer control.Close()
	outerCert := ""
	if !public {
		outerCert = base64.StdEncoding.EncodeToString(s.Certificate().Raw)
	}
	output := map[string]any{"code": code, "outer_certificate": outerCert, "control_url": control.URL, "control_token": key}
	data, err := json.Marshal(output)
	check(err)
	check(os.WriteFile(os.Args[1], data, 0600))
	_, _ = io.Copy(io.Discard, os.Stdin) // parent closes stdin to tear down the fixture
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
