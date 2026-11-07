// Package wecom implements the 企业微信开放平台 smart-bot QR connect flow.
// The protocol mirrors the public reference client (MIT, WecomTeam/wecom-cli
// crates/wecom-cli/src/auth/qrcode.rs): create a QR session, then poll until
// the scan is confirmed and bot credentials are returned.
package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"time"
)

const (
	// Source identifies the connecting client family to WeCom; reusing the
	// reference client's value keeps the generate endpoint accepted.
	qrSource = "wecom_cli_external"

	qrGeneratePath = "/ai/qc/generate"
	qrQueryPath    = "/ai/qc/query_result"
	qrPagePath     = "/ai/qc/gen"

	// PollInterval is the server-recommended poll cadence; PollTimeout bounds
	// one login attempt (QR codes expire shortly after five minutes).
	PollInterval = 3 * time.Second
	PollTimeout  = 5 * time.Minute
)

// qrBase is overridable in tests via export_test.go.
var (
	qrBase       = "https://work.weixin.qq.com"
	httpClient   = &http.Client{Timeout: 15 * time.Second}
	pollInterval = PollInterval
	pollTimeout  = PollTimeout
)

// QrSession is one pending QR login. AuthURL is what the QR encodes; PageURL
// renders the same code in a browser for devices without a camera.
type QrSession struct {
	Scode   string
	AuthURL string
	PageURL string
}

// Bot holds the credentials returned after a confirmed scan.
type Bot struct {
	BotID  string
	Secret string
}

// platformCode maps the running OS to the generate endpoint's plat values:
// 1 macOS, 2 Windows, 3 Linux, 0 other.
func platformCode() string {
	switch runtime.GOOS {
	case "darwin":
		return "1"
	case "windows":
		return "2"
	case "linux":
		return "3"
	default:
		return "0"
	}
}

// CreateQrSession asks WeCom for a fresh QR login session.
func CreateQrSession(ctx context.Context) (*QrSession, error) {
	target := qrBase + qrGeneratePath + "?" + url.Values{
		"source": {qrSource},
		"plat":   {platformCode()},
	}.Encode()
	var payload struct {
		Data struct {
			Scode   string `json:"scode"`
			AuthURL string `json:"auth_url"`
		} `json:"data"`
	}
	raw, err := getJSON(ctx, target, &payload)
	if err != nil {
		return nil, fmt.Errorf("创建企业微信扫码会话失败：%w", err)
	}
	if payload.Data.Scode == "" || payload.Data.AuthURL == "" {
		return nil, fmt.Errorf("企业微信扫码会话响应不完整：%s", raw)
	}
	page := qrBase + qrPagePath + "?" + url.Values{
		"source": {qrSource},
		"scode":  {payload.Data.Scode},
	}.Encode()
	return &QrSession{Scode: payload.Data.Scode, AuthURL: payload.Data.AuthURL, PageURL: page}, nil
}

// Wait polls the session until the scan is confirmed, the context is
// cancelled, or the poll timeout elapses.
func (s *QrSession) Wait(ctx context.Context) (Bot, error) {
	deadline := time.Now().Add(pollTimeout)
	target := qrBase + qrQueryPath + "?" + url.Values{"scode": {s.Scode}}.Encode()
	for {
		if time.Now().After(deadline) {
			return Bot{}, errors.New("企业微信扫码确认超时，请重新连接")
		}
		var payload struct {
			Data struct {
				Status  string `json:"status"`
				BotInfo struct {
					BotID  string `json:"botid"`
					Secret string `json:"secret"`
				} `json:"bot_info"`
			} `json:"data"`
		}
		if _, err := getJSON(ctx, target, &payload); err != nil {
			if ctx.Err() != nil {
				return Bot{}, ctx.Err()
			}
			return Bot{}, fmt.Errorf("查询企业微信扫码结果失败：%w", err)
		}
		if payload.Data.Status == "success" {
			info := payload.Data.BotInfo
			if info.BotID == "" || info.Secret == "" {
				return Bot{}, errors.New("扫码成功但未获取到机器人凭据，请重新连接")
			}
			return Bot{BotID: info.BotID, Secret: info.Secret}, nil
		}
		select {
		case <-ctx.Done():
			return Bot{}, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// getJSON fetches target and decodes the body into into; the raw body is
// returned so protocol diagnostics can quote it.
func getJSON(ctx context.Context, target string, into any) (raw []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return body, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return body, json.Unmarshal(body, into)
}
