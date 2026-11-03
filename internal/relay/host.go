package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const DefaultURL = "https://150.5.143.32"

type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}
type savedDevice struct {
	Device
	TokenHash string `json:"token_hash"`
}
type hostState struct {
	Enabled     bool          `json:"enabled"`
	RelayURL    string        `json:"relay_url"`
	RouteID     string        `json:"route_id"`
	HostToken   string        `json:"host_token"`
	Certificate []byte        `json:"certificate"`
	PrivateKey  []byte        `json:"private_key"`
	Devices     []savedDevice `json:"devices"`
}

type PairCode struct {
	Version     int       `json:"v"`
	RelayURL    string    `json:"relay_url"`
	RouteID     string    `json:"route_id"`
	Fingerprint string    `json:"host_pubkey_fp"`
	PairToken   string    `json:"pair_token"`
	HostName    string    `json:"host_name"`
	ExpiresAt   time.Time `json:"expires_at"`
}
type HostStatus struct {
	Enabled  bool     `json:"enabled"`
	Online   bool     `json:"online"`
	RelayURL string   `json:"relay_url"`
	Devices  []Device `json:"devices"`
	Error    string   `json:"error,omitempty"`
	Pairing  bool     `json:"pairing"`
}
type hostConnection struct {
	conn     net.Conn
	deviceID string
}
type deviceContext struct{}

// DeviceID is set only after the dedicated tunnel ingress validates a saved device.
func DeviceID(ctx context.Context) string {
	id, _ := ctx.Value(deviceContext{}).(string)
	return id
}

type Host struct {
	mu           sync.Mutex
	configureMu  sync.Mutex
	state        hostState
	path         string
	handler      http.Handler
	client       *http.Client
	dialer       *websocket.Dialer
	cancel       context.CancelFunc
	online       bool
	lastError    string
	generation   uint64
	connections  map[*hostConnection]struct{}
	pairHash     string
	pairExpiry   time.Time
	pairFailures int
}

func NewHost(path string, handler http.Handler) (*Host, error) {
	h := &Host{path: path, handler: handler, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, dialer: websocket.DefaultDialer, connections: map[*hostConnection]struct{}{}}
	f, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err == nil {
		defer f.Close()
		err = json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&h.state)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if h.state.RelayURL == "" {
		h.state.RelayURL = DefaultURL
	}
	if h.state.RouteID != "" {
		if _, err := NormalizeURL(h.state.RelayURL); err != nil {
			return nil, err
		}
		if _, err := tls.X509KeyPair(h.state.Certificate, h.state.PrivateKey); err != nil {
			return nil, errors.New("invalid saved host identity")
		}
	}
	return h, nil
}

func (h *Host) Status() HostStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := HostStatus{Enabled: h.state.Enabled, Online: h.online, RelayURL: h.state.RelayURL, Error: h.lastError, Devices: []Device{}}
	s.Pairing = h.pairHash != "" && time.Now().Before(h.pairExpiry) && h.pairFailures < 10
	for _, d := range h.state.Devices {
		s.Devices = append(s.Devices, d.Device)
	}
	return s
}

// Configure stores credentials privately before enabling an outbound connection.
// The registration key is used only once and is never retained.
func (h *Host) Configure(ctx context.Context, enabled bool, base, registrationKey string) error {
	h.configureMu.Lock()
	defer h.configureMu.Unlock()
	h.mu.Lock()
	next := h.state
	h.mu.Unlock()
	if enabled {
		var err error
		base, err = NormalizeURL(base)
		if err != nil {
			return err
		}
		if base != next.RelayURL && len(next.Devices) > 0 {
			return errors.New("请先撤销已配对设备，再更换中继地址")
		}
		if next.RouteID == "" || next.RelayURL != base {
			req, err := http.NewRequestWithContext(ctx, "POST", base+"/register", nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+registrationKey)
			response, err := h.client.Do(req)
			if err != nil {
				return errors.New("无法连接中继，请检查地址和网络")
			}
			var registration struct {
				RouteID   string `json:"route_id"`
				HostToken string `json:"host_token"`
			}
			decodeErr := json.NewDecoder(http.MaxBytesReader(nil, response.Body, 8<<10)).Decode(&registration)
			_ = response.Body.Close()
			if response.StatusCode != 201 {
				return fmt.Errorf("中继注册失败（HTTP %d），请检查接入码", response.StatusCode)
			}
			if decodeErr != nil || len(registration.RouteID) != 43 || len(registration.HostToken) != 43 {
				return errors.New("中继返回的注册身份无效")
			}
			next.RouteID, next.HostToken = registration.RouteID, registration.HostToken
		}
		next.RelayURL = base
		if len(next.PrivateKey) == 0 {
			cert, key, err := newIdentity()
			if err != nil {
				return err
			}
			next.Certificate, next.PrivateKey = cert, key
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Pairing/revocation may have updated the device list during registration.
	if enabled && next.RelayURL != h.state.RelayURL && len(h.state.Devices) > 0 {
		return errors.New("请先撤销已配对设备，再更换中继地址")
	}
	next.Devices = slices.Clone(h.state.Devices)
	next.Enabled = enabled
	if err := saveJSON(h.path, next); err != nil {
		return errors.New("无法保存远程访问配置")
	}
	h.stopLocked()
	h.state = next
	if enabled {
		h.startLocked()
	}
	return nil
}

func (h *Host) Start() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state.Enabled && h.cancel == nil {
		h.startLocked()
	}
}

func (h *Host) startLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.generation++
	go h.run(ctx, h.generation, h.state)
}

func (h *Host) stopLocked() {
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	h.generation++
	h.online = false
	h.lastError = ""
	h.pairHash = ""
	for c := range h.connections {
		_ = c.conn.Close()
	}
}

func (h *Host) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopLocked()
	return nil
}

func newIdentity() ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	certificate, err := certificateForKey(key)
	if err != nil {
		return nil, nil, err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return certificate, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), nil
}

func certificateForKey(key *ecdsa.PrivateKey) ([]byte, error) {
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(spki)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	// Apple enforces certificate lifetime limits even for explicitly pinned
	// self-signed leaves. Renew the certificate without rotating its pinned key.
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "EasyAgent paired host"}, DNSNames: []string{virtualName(sum[:])}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func (h *Host) certificateLocked() (tls.Certificate, error) {
	cert, err := tls.X509KeyPair(h.state.Certificate, h.state.PrivateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	if cert.Leaf == nil {
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return tls.Certificate{}, err
		}
	}
	if time.Until(cert.Leaf.NotAfter) >= 7*24*time.Hour && cert.Leaf.NotAfter.Sub(cert.Leaf.NotBefore) <= 90*24*time.Hour {
		return cert, nil
	}
	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return tls.Certificate{}, errors.New("invalid host key type")
	}
	certificate, err := certificateForKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	next := h.state
	next.Certificate = certificate
	if err = saveJSON(h.path, next); err != nil {
		return tls.Certificate{}, err
	}
	h.state = next
	return tls.X509KeyPair(next.Certificate, next.PrivateKey)
}

func virtualName(sum []byte) string { return "ea-" + hex.EncodeToString(sum[:16]) + ".invalid" }

func PairServerName(pin string) (string, error) {
	if !strings.HasPrefix(pin, "sha256:") {
		return "", errors.New("invalid host fingerprint")
	}
	sum, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(pin, "sha256:"))
	if err != nil || len(sum) != 32 {
		return "", errors.New("invalid host fingerprint")
	}
	return virtualName(sum), nil
}

func fingerprint(certPEM []byte) (string, error) {
	p, _ := pem.Decode(certPEM)
	if p == nil {
		return "", errors.New("invalid host certificate")
	}
	c, err := x509.ParseCertificate(p.Bytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return "sha256:" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

func (h *Host) NewPair(name string) (PairCode, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.state.Enabled || !h.online {
		return PairCode{}, errors.New("请先连接中继，再生成配对码")
	}
	if len(h.state.Devices) >= 16 {
		return PairCode{}, errors.New("已达到配对设备上限，请先撤销不再使用的设备")
	}
	pin, err := fingerprint(h.state.Certificate)
	if err != nil {
		return PairCode{}, err
	}
	secret := token()
	h.pairHash = digest(secret)
	h.pairExpiry = time.Now().Add(5 * time.Minute)
	h.pairFailures = 0
	if name == "" {
		name = "EasyAgent"
	}
	return PairCode{Version: 1, RelayURL: h.state.RelayURL, RouteID: h.state.RouteID, Fingerprint: pin, PairToken: secret, HostName: name, ExpiresAt: h.pairExpiry}, nil
}

func (h *Host) CancelPair() {
	h.CancelPairIf("")
}

// CancelPairIf prevents a delayed UI cancellation from consuming a newer code.
// An empty token is the explicit owner operation to cancel any pending code.
func (h *Host) CancelPairIf(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if token == "" || subtle.ConstantTimeCompare([]byte(h.pairHash), []byte(digest(token))) == 1 {
		h.pairHash = ""
	}
}

func (h *Host) Revoke(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	next := h.state
	next.Devices = slices.DeleteFunc(slices.Clone(next.Devices), func(d savedDevice) bool { return d.ID == id })
	if len(next.Devices) == len(h.state.Devices) {
		return errors.New("配对设备不存在")
	}
	if err := saveJSON(h.path, next); err != nil {
		return err
	}
	h.state = next
	for c := range h.connections {
		if c.deviceID == id {
			_ = c.conn.Close()
		}
	}
	return nil
}

func (h *Host) claim(w http.ResponseWriter, r *http.Request, c *hostConnection) {
	var input struct {
		PairToken string `json:"pair_token"`
		Name      string `json:"name"`
	}
	if r.Method != "POST" || decode(w, r, &input) != nil || len(input.Name) > 128 {
		http.Error(w, "invalid pairing request", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	if !h.state.Enabled || h.pairHash == "" || time.Now().After(h.pairExpiry) || h.pairFailures >= 10 || subtle.ConstantTimeCompare([]byte(h.pairHash), []byte(digest(input.PairToken))) != 1 {
		if h.pairHash != "" {
			h.pairFailures++
		}
		h.mu.Unlock()
		http.Error(w, "配对码无效或已过期，请在电脑上重新生成", http.StatusUnauthorized)
		return
	}
	secret := token()
	d := savedDevice{Device: Device{ID: token(), Name: strings.TrimSpace(input.Name), CreatedAt: time.Now().UTC()}, TokenHash: digest(secret)}
	if d.Name == "" {
		d.Name = "手机"
	}
	next := h.state
	next.Devices = append(slices.Clone(next.Devices), d)
	if err := saveJSON(h.path, next); err != nil {
		h.mu.Unlock()
		http.Error(w, "无法保存配对设备", http.StatusServiceUnavailable)
		return
	}
	h.state = next
	h.pairHash = ""
	c.deviceID = d.ID
	// Persist and consume the code atomically, then release the state lock before
	// sending credentials to a potentially slow client.
	h.mu.Unlock()
	reply(w, 201, map[string]any{"device": d.Device, "device_token": secret})
}

func (h *Host) ingress(c *hostConnection) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pair/claim" {
			h.claim(w, r, c)
			return
		}
		if r.URL.Path == "/health" {
			reply(w, 200, map[string]string{"status": "ok", "service": "easyagent-paired-host"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/remote-access") || strings.HasPrefix(r.URL.Path, "/admin/") {
			http.Error(w, "此操作仅限电脑端管理", http.StatusForbidden)
			return
		}
		h.mu.Lock()
		id := ""
		if h.state.Enabled {
			hash := digest(bearer(r))
			for _, d := range h.state.Devices {
				if subtle.ConstantTimeCompare([]byte(hash), []byte(d.TokenHash)) == 1 && (c.deviceID == "" || c.deviceID == d.ID) {
					id = d.ID
					break
				}
			}
		}
		if id != "" {
			c.deviceID = id
		}
		h.mu.Unlock()
		if id == "" {
			http.Error(w, "设备未配对或已撤销，请重新扫码", http.StatusUnauthorized)
			return
		}
		h.handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), deviceContext{}, id)))
	})
}

func (h *Host) setConnectionState(generation uint64, online bool, message string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.generation == generation {
		h.online = online
		h.lastError = message
	}
}

func (h *Host) run(ctx context.Context, generation uint64, state hostState) {
	delay := time.Second
	slots := make(chan struct{}, 16)
	for ctx.Err() == nil {
		headers := http.Header{"Authorization": {"Bearer " + state.HostToken}}
		ws, response, err := h.dialer.DialContext(ctx, socketURL(state.RelayURL, "/host", url.Values{"route_id": {state.RouteID}}), headers)
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err == nil {
			h.setConnectionState(generation, true, "")
			delay = time.Second
			conn := newControl(ws)
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			for {
				var msg struct {
					Type         string `json:"type"`
					ConnectionID string `json:"connection_id"`
				}
				if err = ws.ReadJSON(&msg); err != nil {
					break
				}
				if msg.Type != "open" || len(msg.ConnectionID) != 43 {
					continue
				}
				select {
				case slots <- struct{}{}:
					go func(id string) { defer func() { <-slots }(); h.serveStream(ctx, generation, state, id) }(msg.ConnectionID)
				default:
				}
			}
			stop()
			_ = conn.Close()
		}
		message := "中继连接中断，正在自动重连"
		if response != nil && response.StatusCode == 401 {
			message = "中继身份验证失败，请检查服务端注册数据"
		}
		h.setConnectionState(generation, false, message)
		jitter, jitterErr := rand.Int(rand.Reader, big.NewInt(500))
		if jitterErr != nil {
			jitter = big.NewInt(0)
		}
		t := time.NewTimer(delay + time.Duration(jitter.Int64())*time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func (h *Host) serveStream(ctx context.Context, generation uint64, state hostState, id string) {
	ws, response, err := h.dialer.DialContext(ctx, socketURL(state.RelayURL, "/host/stream", url.Values{"route_id": {state.RouteID}, "connection_id": {id}}), http.Header{"Authorization": {"Bearer " + state.HostToken}})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return
	}
	raw := newStream(ws)
	defer raw.Close()
	managed := &hostConnection{conn: raw}
	h.mu.Lock()
	if h.generation != generation || !h.state.Enabled {
		h.mu.Unlock()
		return
	}
	cert, err := h.certificateLocked()
	if err != nil {
		h.lastError = "电脑端加密证书无法续期，请检查数据目录"
		h.mu.Unlock()
		return
	}
	h.connections[managed] = struct{}{}
	h.mu.Unlock()
	conn := tls.Server(raw, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}})
	defer func() { h.mu.Lock(); delete(h.connections, managed); h.mu.Unlock() }()
	listener := &oneListener{conn: conn, done: make(chan struct{})}
	server := &http.Server{Handler: h.ingress(managed), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close(); _ = server.Close(); _ = listener.Close() })
	defer stop()
	_ = server.Serve(listener)
}

type oneListener struct {
	conn     net.Conn
	mu       sync.Mutex
	accepted bool
	done     chan struct{}
	once     sync.Once
}

func (l *oneListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.accepted {
		l.accepted = true
		l.mu.Unlock()
		return &oneConn{Conn: l.conn, closed: func() { _ = l.Close() }}, nil
	}
	l.mu.Unlock()
	<-l.done
	return nil, net.ErrClosed
}
func (l *oneListener) Close() error {
	l.once.Do(func() { close(l.done); _ = l.conn.Close() })
	return nil
}
func (l *oneListener) Addr() net.Addr { return l.conn.LocalAddr() }

type oneConn struct {
	net.Conn
	closed func()
}

func (c *oneConn) Close() error { err := c.Conn.Close(); c.closed(); return err }
