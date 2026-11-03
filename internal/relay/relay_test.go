package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type relayFixture struct {
	b         *Broker
	server    *httptest.Server
	h         *Host
	code      PairCode
	client    *http.Client
	dialInner func(context.Context, string, string) (net.Conn, error)
}

func newRelayFixture(t *testing.T) *relayFixture {
	t.Helper()
	key := strings.Repeat("test-registration-", 3)
	b, err := NewBroker(BrokerConfig{StatePath: filepath.Join(t.TempDir(), "routes.json"), RegistrationKey: key})
	require.NoError(t, err)
	s := httptest.NewTLSServer(b.Handler())
	t.Cleanup(s.Close)
	t.Cleanup(func() { _ = b.Close() })
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if DeviceID(r.Context()) == "" {
			http.Error(w, "missing device identity", 500)
			return
		}
		if r.URL.Path == "/ws" {
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
		}
		reply(w, 200, map[string]string{"device_id": DeviceID(r.Context()), "path": r.URL.Path})
	})
	h, err := NewHost(filepath.Join(t.TempDir(), "host.json"), handler)
	require.NoError(t, err)
	h.client = s.Client()
	h.dialer = &websocket.Dialer{TLSClientConfig: s.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	t.Cleanup(func() { _ = h.Close() })
	require.NoError(t, h.Configure(context.Background(), true, s.URL, key))
	require.Eventually(t, func() bool { return h.Status().Online }, 3*time.Second, 10*time.Millisecond)
	code, err := h.NewPair("test computer")
	require.NoError(t, err)
	name, err := PairServerName(code.Fingerprint)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(h.state.Certificate))
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		ws, response, err := h.dialer.DialContext(ctx, socketURL(s.URL, "/client", url.Values{"route_id": {code.RouteID}}), nil)
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			return nil, err
		}
		raw := newStream(ws)
		conn := tls.Client(raw, &tls.Config{RootCAs: roots, ServerName: name, MinVersion: tls.VersionTLS12})
		if err = conn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return conn, nil
	}
	transport := &http.Transport{DialTLSContext: dial, MaxConnsPerHost: 8}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(transport.CloseIdleConnections)
	return &relayFixture{b: b, server: s, h: h, code: code, client: client, dialInner: dial}
}

func (f *relayFixture) request(t *testing.T, path, secret string, body any) (int, []byte) {
	t.Helper()
	var input io.Reader
	method := "GET"
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		input = bytes.NewReader(data)
		method = "POST"
	}
	name, err := PairServerName(f.code.Fingerprint)
	require.NoError(t, err)
	req, err := http.NewRequest(method, "https://"+name+path, input)
	require.NoError(t, err)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	response, err := f.client.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, data
}

func (f *relayFixture) pair(t *testing.T) (Device, string) {
	t.Helper()
	status, data := f.request(t, "/pair/claim", "", map[string]string{"pair_token": f.code.PairToken, "name": "phone"})
	require.Equal(t, 201, status, string(data))
	var result struct {
		Device Device `json:"device"`
		Token  string `json:"device_token"`
	}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Len(t, result.Token, 43)
	return result.Device, result.Token
}

func TestRelayTLSRequestPairingRevocationAndPersistence(t *testing.T) {
	f := newRelayFixture(t)
	status, _ := f.request(t, "/sessions", "", nil)
	require.Equal(t, 401, status)
	device, secret := f.pair(t)
	status, body := f.request(t, "/sessions", secret, nil)
	require.Equal(t, 200, status, string(body))
	require.Contains(t, string(body), device.ID)
	status, _ = f.request(t, "/remote-access", secret, nil)
	require.Equal(t, 403, status)
	status, _ = f.request(t, "/pair/claim", "", map[string]string{"pair_token": f.code.PairToken})
	require.Equal(t, 401, status)
	stored, err := os.ReadFile(f.h.path)
	require.NoError(t, err)
	require.NotContains(t, string(stored), secret)
	info, err := os.Stat(f.h.path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	brokerState, err := os.ReadFile(f.b.cfg.StatePath)
	require.NoError(t, err)
	require.NotContains(t, string(brokerState), f.h.state.HostToken)
	reloaded, err := NewBroker(f.b.cfg)
	require.NoError(t, err)
	defer reloaded.Close()
	require.Equal(t, f.b.routes[f.code.RouteID], reloaded.routes[f.code.RouteID])
	restored, err := NewHost(f.h.path, f.h.handler)
	require.NoError(t, err)
	require.Equal(t, f.h.Status().Devices, restored.Status().Devices)
	require.NoError(t, f.h.Revoke(device.ID))
	status, _ = f.request(t, "/sessions", secret, nil)
	require.Equal(t, 401, status)
	require.Empty(t, f.h.Status().Devices)
}

func TestRelayRevocationClosesExistingWebSocket(t *testing.T) {
	f := newRelayFixture(t)
	device, secret := f.pair(t)
	name, err := PairServerName(f.code.Fingerprint)
	require.NoError(t, err)
	d := &websocket.Dialer{NetDialTLSContext: f.dialInner}
	ws, response, err := d.Dial("wss://"+name+"/ws", http.Header{"Authorization": {"Bearer " + secret}})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	defer ws.Close()
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte("approval")))
	_, data, err := ws.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, "approval", string(data))
	require.NoError(t, f.h.Revoke(device.ID))
	require.NoError(t, ws.SetReadDeadline(time.Now().Add(time.Second)))
	_, _, err = ws.ReadMessage()
	require.Error(t, err)
}

func TestPairCodeExpiresCancelsAndIsConsumedAtomically(t *testing.T) {
	f := newRelayFixture(t)
	f.h.mu.Lock()
	f.h.pairExpiry = time.Now().Add(-time.Second)
	f.h.mu.Unlock()
	status, _ := f.request(t, "/pair/claim", "", map[string]string{"pair_token": f.code.PairToken})
	require.Equal(t, 401, status)
	code, err := f.h.NewPair("test")
	require.NoError(t, err)
	f.h.CancelPair()
	status, _ = f.request(t, "/pair/claim", "", map[string]string{"pair_token": code.PairToken})
	require.Equal(t, 401, status)
	code, err = f.h.NewPair("test")
	require.NoError(t, err)
	previous := code
	code, err = f.h.NewPair("newer pairing")
	require.NoError(t, err)
	f.h.CancelPairIf(previous.PairToken)
	require.True(t, f.h.Status().Pairing, "late cancellation must not cancel a newer QR")
	f.code = code
	var successes atomic.Int32
	var wg sync.WaitGroup
	name, err := PairServerName(code.Fingerprint)
	require.NoError(t, err)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, _ := json.Marshal(map[string]string{"pair_token": code.PairToken, "name": "phone"})
			r, err := f.client.Post("https://"+name+"/pair/claim", "application/json", bytes.NewReader(body))
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			if r.StatusCode == 201 {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, successes.Load())
	require.Len(t, f.h.Status().Devices, 1)
}

func TestBrokerRejectsUnknownHostAndCrossRouteStream(t *testing.T) {
	f := newRelayFixture(t)
	for _, path := range []string{"/host?route_id=" + f.code.RouteID, "/host/stream?route_id=" + f.code.RouteID + "&connection_id=other"} {
		u := "wss" + strings.TrimPrefix(f.server.URL, "https") + path
		ws, response, err := f.h.dialer.Dial(u, http.Header{"Authorization": {"Bearer wrong"}})
		if ws != nil {
			_ = ws.Close()
		}
		require.Error(t, err)
		require.NotNil(t, response)
		require.Equal(t, 401, response.StatusCode)
		_ = response.Body.Close()
	}
	r, err := http.NewRequest("POST", f.server.URL+"/register", nil)
	require.NoError(t, err)
	response, err := f.server.Client().Do(r)
	require.NoError(t, err)
	require.Equal(t, 401, response.StatusCode)
	_ = response.Body.Close()
}

func TestRelayDisableClosesConnectionsAndKeepsPairingOnReenable(t *testing.T) {
	f := newRelayFixture(t)
	_, secret := f.pair(t)
	conn, err := f.dialInner(context.Background(), "tcp", "")
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, f.h.Configure(context.Background(), false, "", ""))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = conn.Read(make([]byte, 1))
	require.Error(t, err)
	require.False(t, f.h.Status().Enabled)
	_, err = f.h.NewPair("phone")
	require.Error(t, err)
	restored, err := NewHost(f.h.path, f.h.handler)
	require.NoError(t, err)
	require.False(t, restored.Status().Enabled)
	require.Len(t, restored.Status().Devices, 1)
	require.NoError(t, f.h.Configure(context.Background(), true, f.server.URL, ""))
	require.Eventually(t, func() bool { return f.h.Status().Online }, 3*time.Second, 10*time.Millisecond)
	status, _ := f.request(t, "/sessions", secret, nil)
	require.Equal(t, 200, status)
}

func TestHostCertificateRenewsWithoutChangingPinnedKey(t *testing.T) {
	f := newRelayFixture(t)
	f.h.mu.Lock()
	cert, err := tls.X509KeyPair(f.h.state.Certificate, f.h.state.PrivateKey)
	if err != nil {
		f.h.mu.Unlock()
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		f.h.mu.Unlock()
		t.Fatal(err)
	}
	leaf.NotAfter = time.Now().Add(time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, leaf.PublicKey, cert.PrivateKey)
	if err != nil {
		f.h.mu.Unlock()
		t.Fatal(err)
	}
	f.h.state.Certificate = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	renewed, err := f.h.certificateLocked()
	identity := slices.Clone(f.h.state.Certificate)
	f.h.mu.Unlock()
	require.NoError(t, err)
	require.Greater(t, time.Until(renewed.Leaf.NotAfter), 29*24*time.Hour)
	pin, err := fingerprint(identity)
	require.NoError(t, err)
	require.Equal(t, f.code.Fingerprint, pin)
	restored, err := NewHost(f.h.path, f.h.handler)
	require.NoError(t, err)
	pin, err = fingerprint(restored.state.Certificate)
	require.NoError(t, err)
	require.Equal(t, f.code.Fingerprint, pin)
}
