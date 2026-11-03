package relay

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type BrokerConfig struct {
	StatePath       string
	RegistrationKey string
	MaxRoutes       int
	MaxStreams      int
	BytesPerSecond  int
	Version         string
}

type brokerHost struct {
	control *websocket.Conn
	writeMu sync.Mutex
	streams map[string]*brokerStream
}
type brokerStream struct {
	client  *websocket.Conn
	host    *websocket.Conn
	ready   chan struct{}
	joining bool
}

// Broker keeps registration identities on disk and only live connections in memory.
type Broker struct {
	cfg         BrokerConfig
	mu          sync.Mutex
	routes      map[string]string
	hosts       map[string]*brokerHost
	registers   map[string]time.Time
	clients     map[string]int
	closed      bool
	streamCount int
}

func NewBroker(cfg BrokerConfig) (*Broker, error) {
	if cfg.StatePath == "" || len(cfg.RegistrationKey) < 32 {
		return nil, errors.New("private state path and a registration key of at least 32 characters are required")
	}
	if cfg.MaxRoutes <= 0 {
		cfg.MaxRoutes = 256
	}
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = 16
	}
	if cfg.BytesPerSecond <= 0 {
		cfg.BytesPerSecond = 4 << 20
	}
	b := &Broker{cfg: cfg, routes: map[string]string{}, hosts: map[string]*brokerHost{}, registers: map[string]time.Time{}, clients: map[string]int{}}
	data, err := os.ReadFile(cfg.StatePath)
	if err == nil {
		err = json.Unmarshal(data, &b.routes)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if b.routes == nil {
		return nil, errors.New("invalid relay registration store")
	}
	return b, nil
}

func (b *Broker) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"service": "easyagent-relay", "protocol": 1, "version": b.cfg.Version})
	})
	m.HandleFunc("POST /register", b.register)
	m.HandleFunc("GET /host", b.host)
	m.HandleFunc("GET /host/stream", b.hostStream)
	m.HandleFunc("GET /client", b.client)
	return m
}

func (b *Broker) register(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(bearer(r)), []byte(b.cfg.RegistrationKey)) != 1 {
		http.Error(w, "registration denied", http.StatusUnauthorized)
		return
	}
	ip := clientIP(r)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || len(b.routes) >= b.cfg.MaxRoutes {
		http.Error(w, "registration unavailable", http.StatusServiceUnavailable)
		return
	}
	if last := b.registers[ip]; time.Since(last) < time.Second {
		http.Error(w, "registration throttled", http.StatusTooManyRequests)
		return
	}
	id, secret := token(), token()
	b.routes[id] = digest(secret)
	if err := saveJSON(b.cfg.StatePath, b.routes); err != nil {
		delete(b.routes, id)
		http.Error(w, "registration unavailable", http.StatusServiceUnavailable)
		return
	}
	// This map is bounded by the number of registrations.
	b.registers[ip] = time.Now()
	reply(w, 201, map[string]string{"route_id": id, "host_token": secret})
}

func (b *Broker) authenticated(r *http.Request) bool {
	expected, ok := b.routes[r.URL.Query().Get("route_id")]
	return ok && subtle.ConstantTimeCompare([]byte(expected), []byte(digest(bearer(r)))) == 1
}

// Only the local TLS terminator may supply the original peer address.
func clientIP(r *http.Request) string {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if peer := net.ParseIP(ip); peer != nil && peer.IsLoopback() {
		if forwarded := net.ParseIP(r.Header.Get("X-Real-IP")); forwarded != nil {
			return forwarded.String()
		}
	}
	return ip
}

var brokerUpgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	ReadBufferSize:   4096, WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
}

func (b *Broker) host(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	allowed := !b.closed && b.authenticated(r)
	b.mu.Unlock()
	if !allowed {
		http.Error(w, "host denied", http.StatusUnauthorized)
		return
	}
	ws, err := brokerUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	control := newControl(ws)
	defer control.Close()
	id := r.URL.Query().Get("route_id")
	h := &brokerHost{control: ws, streams: map[string]*brokerStream{}}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	old := b.hosts[id]
	b.hosts[id] = h
	b.mu.Unlock()
	if old != nil {
		b.closeHost(old)
	}
	defer func() {
		b.mu.Lock()
		if b.hosts[id] == h {
			delete(b.hosts, id)
		}
		b.mu.Unlock()
		b.closeHost(h)
	}()
	// The host sends only heartbeats on this control connection.
	for {
		_, _, err := ws.ReadMessage()
		if err != nil {
			return
		}
	}
}

func (b *Broker) closeHost(h *brokerHost) {
	_ = h.control.Close()
	b.mu.Lock()
	var connections []*websocket.Conn
	for _, s := range h.streams {
		if s.client != nil {
			connections = append(connections, s.client)
		}
		if s.host != nil {
			connections = append(connections, s.host)
		}
	}
	b.mu.Unlock()
	for _, c := range connections {
		_ = c.Close()
	}
}

func (b *Broker) hostStream(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	h := b.hosts[r.URL.Query().Get("route_id")]
	var s *brokerStream
	if h != nil {
		s = h.streams[r.URL.Query().Get("connection_id")]
	}
	allowed := !b.closed && b.authenticated(r) && s != nil && s.host == nil && !s.joining
	if !allowed {
		b.mu.Unlock()
		http.Error(w, "stream denied", http.StatusUnauthorized)
		return
	}
	// Never hold the broker lock across network writes. Reserve the join first,
	// then recheck that the same host generation still owns the stream.
	s.joining = true
	b.mu.Unlock()
	ws, err := brokerUpgrader.Upgrade(w, r, nil)
	if err != nil {
		b.mu.Lock()
		s.joining = false
		b.mu.Unlock()
		return
	}
	b.mu.Lock()
	if b.closed || b.hosts[r.URL.Query().Get("route_id")] != h || h.streams[r.URL.Query().Get("connection_id")] != s {
		b.mu.Unlock()
		_ = ws.Close()
		return
	}
	s.host = ws
	close(s.ready)
	b.mu.Unlock()
	// The client handler owns both sockets and releases them together.
}

func (b *Broker) client(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	id := r.URL.Query().Get("route_id")
	b.mu.Lock()
	h := b.hosts[id]
	if b.closed || h == nil {
		b.mu.Unlock()
		http.Error(w, "host offline", http.StatusServiceUnavailable)
		return
	}
	if len(h.streams) >= b.cfg.MaxStreams || b.clients[ip] >= 32 || b.streamCount >= 128 {
		b.mu.Unlock()
		http.Error(w, "connection limit", http.StatusTooManyRequests)
		return
	}
	s := &brokerStream{ready: make(chan struct{})}
	connID := token()
	h.streams[connID] = s
	b.clients[ip]++
	b.streamCount++
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(h.streams, connID)
		b.clients[ip]--
		b.streamCount--
		if b.clients[ip] == 0 {
			delete(b.clients, ip)
		}
		host, client := s.host, s.client
		b.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		if host != nil {
			_ = host.Close()
		}
	}()
	ws, err := brokerUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	b.mu.Lock()
	if b.closed || b.hosts[id] != h {
		b.mu.Unlock()
		_ = ws.Close()
		return
	}
	s.client = ws
	b.mu.Unlock()
	h.writeMu.Lock()
	_ = h.control.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err = h.control.WriteJSON(map[string]string{"type": "open", "connection_id": connID})
	h.writeMu.Unlock()
	if err != nil {
		return
	}
	select {
	case <-s.ready:
	case <-time.After(10 * time.Second):
		return
	case <-r.Context().Done():
		return
	}
	b.mu.Lock()
	host := s.host
	b.mu.Unlock()
	a, z := newStream(ws), newStream(host)
	defer a.Close()
	defer z.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	done := make(chan struct{}, 2)
	pump := func(dst, src *streamConn) {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32<<10)
		next := time.Now()
		for {
			n, err := src.Read(buf)
			if n > 0 {
				next = maxTime(next, time.Now()).Add(time.Duration(n) * time.Second / time.Duration(b.cfg.BytesPerSecond))
				timer := time.NewTimer(time.Until(next))
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return
				}
				if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go pump(a, z)
	go pump(z, a)
	<-done
	cancel()
	_ = a.Close()
	_ = z.Close()
	<-done
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (b *Broker) Close() error {
	b.mu.Lock()
	b.closed = true
	hosts := make([]*brokerHost, 0, len(b.hosts))
	for _, h := range b.hosts {
		hosts = append(hosts, h)
	}
	b.mu.Unlock()
	for _, h := range hosts {
		b.closeHost(h)
	}
	return nil
}

var _ io.Closer = (*Broker)(nil)
