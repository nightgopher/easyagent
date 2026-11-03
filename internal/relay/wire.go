// Package relay carries end-to-end TLS streams over outbound WebSockets.
// The broker never terminates the inner TLS connection or receives device credentials.
package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const MaxFrame = 64 << 10

func token() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func saveJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".relay-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// NormalizeURL accepts only a TLS-protected broker, without user info or query credentials.
func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("中继地址必须为不带路径、凭据和参数的 HTTPS 地址")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func socketURL(base, path string, values url.Values) string {
	return "wss" + strings.TrimPrefix(base, "https") + path + "?" + values.Encode()
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func decode(w http.ResponseWriter, r *http.Request, target any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

// streamConn maps a single virtual TCP connection to binary WebSocket messages.
// HTTP, TLS and WebSocket upgrades inside this stream retain their usual semantics.
type socketControl struct {
	ws   *websocket.Conn
	once sync.Once
	done chan struct{}
}

func newControl(ws *websocket.Conn) *socketControl {
	c := &socketControl{ws: ws, done: make(chan struct{})}
	ws.SetReadLimit(MaxFrame)
	_ = ws.SetReadDeadline(time.Now().Add(70 * time.Second))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(70 * time.Second)) })
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-ticker.C:
				if ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
					_ = c.Close()
					return
				}
			}
		}
	}()
	return c
}
func (c *socketControl) Close() error {
	c.once.Do(func() { close(c.done); _ = c.ws.Close() })
	return nil
}

type streamConn struct {
	*socketControl
	incoming        chan []byte
	readErr         error
	buffer          []byte
	readMu          sync.Mutex
	writeMu         sync.Mutex
	deadlineMu      sync.Mutex
	readDeadline    time.Time
	deadlineChanged chan struct{}
}

func newStream(ws *websocket.Conn) *streamConn {
	c := &streamConn{socketControl: newControl(ws), incoming: make(chan []byte, 1), deadlineChanged: make(chan struct{})}
	// WebSocket read errors are terminal. Emulate net.Conn read deadlines above
	// this bounded reader, so net/http can cancel a background read during Hijack
	// without permanently poisoning the underlying WebSocket connection.
	go func() {
		defer close(c.incoming)
		for {
			kind, data, err := ws.ReadMessage()
			if err != nil {
				c.readErr = err
				return
			}
			if kind != websocket.BinaryMessage {
				c.readErr = errors.New("relay requires binary frames")
				_ = c.Close()
				return
			}
			if len(data) == 0 {
				continue
			}
			select {
			case c.incoming <- data:
			case <-c.done:
				c.readErr = net.ErrClosed
				return
			}
		}
	}()
	return c
}

func (c *streamConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for {
		c.deadlineMu.Lock()
		deadline, changed := c.readDeadline, c.deadlineChanged
		c.deadlineMu.Unlock()
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return 0, os.ErrDeadlineExceeded
		}
		if len(c.buffer) > 0 {
			n := copy(p, c.buffer)
			c.buffer = c.buffer[n:]
			return n, nil
		}
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case data, ok := <-c.incoming:
			if timer != nil {
				timer.Stop()
			}
			if !ok {
				if c.readErr != nil {
					return 0, c.readErr
				}
				return 0, io.EOF
			}
			c.buffer = data
		case <-changed:
			if timer != nil {
				timer.Stop()
			}
		case <-timeout:
			return 0, os.ErrDeadlineExceeded
		}
	}
}
func (c *streamConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	written := 0
	for len(p) > 0 {
		n := min(len(p), 32<<10)
		if err := c.ws.WriteMessage(websocket.BinaryMessage, p[:n]); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}
func (c *streamConn) LocalAddr() net.Addr  { return c.ws.LocalAddr() }
func (c *streamConn) RemoteAddr() net.Addr { return c.ws.RemoteAddr() }
func (c *streamConn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}
func (c *streamConn) SetReadDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	c.readDeadline = t
	close(c.deadlineChanged)
	c.deadlineChanged = make(chan struct{})
	return nil
}
func (c *streamConn) SetWriteDeadline(t time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.SetWriteDeadline(t)
}
