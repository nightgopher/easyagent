package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

const edgeSpeechToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"

var edgeVoiceID = regexp.MustCompile(`^[a-zA-Z0-9-]{1,100}$`)

// Edge Read Aloud is an unofficial online protocol; its endpoint may change.
// Framing follows thangvv-tech/edge-tts (MIT), with the current service endpoint.
func edgeSpeech(ctx context.Context, text, voice string) ([]byte, error) {
	if !edgeVoiceID.MatchString(voice) {
		return nil, errors.New("无效音色")
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 30000 {
		return nil, errors.New("朗读内容为空或过长")
	}
	var result bytes.Buffer
	for len(text) > 0 {
		n := min(len(text), 2400)
		for n > 0 && !utf8.ValidString(text[:n]) {
			n--
		}
		if n == 0 {
			return nil, errors.New("朗读内容无效")
		}
		if err := edgeSpeechChunk(ctx, text[:n], voice, &result); err != nil {
			return nil, err
		}
		if result.Len() > 12<<20 {
			return nil, errors.New("朗读音频过大")
		}
		text = text[n:]
	}
	return result.Bytes(), nil
}

func edgeSpeechChunk(ctx context.Context, text, voice string, out *bytes.Buffer) error {
	ticks := (time.Now().Unix() + 11644473600) / 300 * 300 * 10000000
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d%s", ticks, edgeSpeechToken)))
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	id := hex.EncodeToString(nonce)
	u := url.URL{Scheme: "wss", Host: "api.msedgeservices.com", Path: "/tts/cognitiveservices/websocket/v1"}
	q := u.Query()
	q.Set("Ocp-Apim-Subscription-Key", edgeSpeechToken)
	q.Set("ConnectionId", id)
	q.Set("Sec-MS-GEC", strings.ToUpper(hex.EncodeToString(digest[:])))
	q.Set("Sec-MS-GEC-Version", "1-155.0.4283.45")
	u.RawQuery = q.Encode()
	h := http.Header{}
	h.Set("Origin", "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold")
	h.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36 Edg/155.0.0.0")
	h.Set("Cookie", "muid="+strings.ToUpper(id)+";")
	h.Set("Cache-Control", "no-cache")
	var conn *websocket.Conn
	var resp *http.Response
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		conn, resp, err = websocket.DefaultDialer.DialContext(ctx, u.String(), h)
		if err == nil {
			break
		}
		if resp != nil {
			if resp.Body != nil {
				resp.Body.Close()
			}
			break
		}
		var network net.Error
		if ctx.Err() != nil || !(errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.As(err, &network) && network.Timeout()) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 300 * time.Millisecond):
		}
	}
	if err != nil {
		return edgeConnectionError(err, resp)
	}
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	ts := time.Now().UTC().Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)")
	config := fmt.Sprintf("X-RequestId:%s\r\nContent-Type:application/json; charset=utf-8\r\nX-Timestamp:%sZ\r\nPath:speech.config\r\n\r\n%s", id, ts, `{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":"false","wordBoundaryEnabled":"false"},"outputFormat":"audio-24khz-48kbitrate-mono-mp3"}}}}`)
	var escaped bytes.Buffer
	if err := xml.EscapeText(&escaped, []byte(text)); err != nil {
		return err
	}
	ssml := fmt.Sprintf(`<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xml:lang="zh-CN"><voice name="%s"><prosody pitch="+0Hz" rate="+0%%" volume="+0%%">%s</prosody></voice></speak>`, voice, escaped.String())
	message := fmt.Sprintf("X-RequestId:%s\r\nContent-Type:application/ssml+xml\r\nX-Timestamp:%sZ\r\nPath:ssml\r\n\r\n%s", id, ts, ssml)
	if err = conn.WriteMessage(websocket.TextMessage, []byte(config)); err != nil {
		return err
	}
	if err = conn.WriteMessage(websocket.TextMessage, []byte(message)); err != nil {
		return err
	}
	initial := out.Len()
	conn.SetReadLimit(1 << 20)
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return errors.New("Edge 语音接收失败")
		}
		if kind == websocket.TextMessage && bytes.Contains(data, []byte("Path:turn.end")) {
			if out.Len() == initial {
				return errors.New("Edge 未返回语音")
			}
			return nil
		}
		if kind == websocket.BinaryMessage && len(data) >= 2 {
			n := int(binary.BigEndian.Uint16(data[:2]))
			if n <= len(data)-2 && bytes.Contains(data[2:2+n], []byte("Path:audio")) {
				if out.Len()+len(data)-2-n > 12<<20 {
					return errors.New("朗读音频过大")
				}
				out.Write(data[2+n:])
			}
		}
	}
}

func edgeConnectionError(err error, resp *http.Response) error {
	if resp != nil {
		return fmt.Errorf("Edge 服务拒绝连接（HTTP %d）", resp.StatusCode)
	}
	var cert x509.UnknownAuthorityError
	var dns *net.DNSError
	var netErr net.Error
	switch {
	case errors.As(err, &cert):
		return errors.New("Edge 证书验证失败")
	case errors.As(err, &dns):
		return errors.New("Edge 域名解析失败")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return errors.New("连接 Edge 超时")
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.EOF):
		return errors.New("Edge 连接被重置")
	default:
		return errors.New("无法连接 Edge 语音服务，请检查网络或切换系统语音")
	}
}
