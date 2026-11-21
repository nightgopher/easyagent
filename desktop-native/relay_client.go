package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type relayConfig struct {
	RelayURL    string `json:"relay_url"`
	RouteID     string `json:"route_id"`
	Fingerprint string `json:"host_pubkey_fp"`
}
type relayPairCode struct {
	Version int `json:"v"`
	relayConfig
	PairToken string    `json:"pair_token"`
	HostName  string    `json:"host_name"`
	ExpiresAt time.Time `json:"expires_at"`
}

var relaySecretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func (r relayConfig) validate() (string, []byte, error) {
	u, err := url.Parse(strings.TrimSpace(r.RelayURL))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !relaySecretPattern.MatchString(r.RouteID) {
		return "", nil, errors.New("配对中继地址或路由无效")
	}
	if !strings.HasPrefix(r.Fingerprint, "sha256:") {
		return "", nil, errors.New("电脑身份指纹无效")
	}
	pin, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.Fingerprint, "sha256:"))
	if err != nil || len(pin) != 32 {
		return "", nil, errors.New("电脑身份指纹无效")
	}
	return "ea-" + hex.EncodeToString(pin[:16]) + ".invalid", pin, nil
}
func parseRelayPair(raw string, now time.Time) (relayPairCode, error) {
	var code relayPairCode
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &code) != nil || code.Version != 1 || !relaySecretPattern.MatchString(code.PairToken) || !code.ExpiresAt.After(now) || code.ExpiresAt.After(now.Add(10*time.Minute)) {
		return code, errors.New("配对码无效或已过期，请在电脑上重新生成")
	}
	_, _, err := code.relayConfig.validate()
	return code, err
}

type relayConnectError struct {
	message string
	cause   error
}

func (e *relayConnectError) Error() string { return e.message }
func (e *relayConnectError) Unwrap() error { return e.cause }
func relayFailure(stage string, err error) error {
	detail := "连接失败"
	var dns *net.DNSError
	var cert x509.UnknownAuthorityError
	var host x509.HostnameError
	var netErr net.Error
	switch {
	case errors.As(err, &dns):
		detail = "域名解析失败"
	case errors.As(err, &cert), errors.As(err, &host):
		detail = "证书验证失败"
	case errors.Is(err, context.DeadlineExceeded):
		detail = "连接超时"
	case errors.As(err, &netErr) && netErr.Timeout():
		detail = "连接超时"
	}
	return &relayConnectError{message: stage + "：" + detail, cause: err}
}

func (r relayConfig) dialTLS(ctx context.Context, outer *websocket.Dialer) (net.Conn, error) {
	name, pin, err := r.validate()
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(strings.TrimRight(r.RelayURL, "/") + "/client")
	u.Scheme = "wss"
	q := u.Query()
	q.Set("route_id", r.RouteID)
	u.RawQuery = q.Encode()
	var ws *websocket.Conn
	var res *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		ws, res, err = outer.DialContext(attemptCtx, u.String(), nil)
		cancel()
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		var netErr net.Error
		if err == nil || res != nil || ctx.Err() != nil || !(errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())) {
			break
		}
	}
	if err != nil {
		if res != nil {
			return nil, &relayConnectError{message: fmt.Sprintf("中继服务器拒绝连接（HTTP %d）", res.StatusCode), cause: err}
		}
		return nil, relayFailure("连接中继服务器失败", err)
	}
	raw := newStream(ws)
	// The computer presents a self-signed certificate. Verify its public key,
	// hostname and validity against the identity obtained out-of-band in the QR.
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: name, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("电脑未提供证书")
		}
		cert := cs.PeerCertificates[0]
		if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
			return errors.New("电脑证书已过期")
		}
		if err := cert.VerifyHostname(name); err != nil {
			return err
		}
		sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
		if subtle.ConstantTimeCompare(sum[:], pin) != 1 {
			return errors.New("电脑身份已变化，请重新扫码")
		}
		return nil
	}}
	c := tls.Client(raw, cfg)
	if err = c.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, relayFailure("验证电脑身份失败", err)
	}
	return c, nil
}
func newRelayBackend(r relayConfig, token string, outer *websocket.Dialer) (*backendClient, error) {
	name, _, err := r.validate()
	if err != nil {
		return nil, err
	}
	b, err := newBackendClient("https://"+name, token)
	if err != nil {
		return nil, err
	}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" || addr != net.JoinHostPort(name, "443") {
			return nil, errors.New("无效中继目标")
		}
		return r.dialTLS(ctx, outer)
	}
	b.HTTP.Transport = &http.Transport{DialTLSContext: dial, MaxConnsPerHost: 8, ForceAttemptHTTP2: false}
	b.viaRelay = true
	b.socketDialer = &websocket.Dialer{NetDialTLSContext: dial, HandshakeTimeout: 15 * time.Second}
	return b, nil
}
