package larkwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrBusy 表示目标会话有任务在跑（核心 409 busy）。
var ErrBusy = errors.New("session busy")

// ErrDuplicate 表示同一 request_id 已被处理过（核心 409 request_conflict）。
var ErrDuplicate = errors.New("duplicate request")

// Injector 通过核心 HTTP API 把提示注入管家会话。
type Injector struct {
	// BaseURL 是核心服务地址。
	BaseURL string
	// APIKey 是 Bearer 令牌；为空时不带鉴权头。
	APIKey string
	// HTTP 是客户端；为 nil 时用 5 分钟超时的默认客户端。
	HTTP *http.Client
}

func (i *Injector) client() *http.Client {
	if i.HTTP != nil {
		return i.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (i *Injector) post(ctx context.Context, path string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(i.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if i.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+i.APIKey)
	}
	return i.client().Do(req)
}

// Inject 把提示作为一条新任务投递给会话，阻塞到任务结束并返回最终文本。
func (i *Injector) Inject(ctx context.Context, sessionID, prompt, requestID string) (string, error) {
	resp, err := i.post(ctx, "/chat", map[string]string{
		"prompt":     prompt,
		"session_id": sessionID,
		"channel":    "larkwatch",
		"request_id": requestID,
	})
	if err != nil {
		return "", fmt.Errorf("post /chat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&failure)
		switch failure.Code {
		case "busy":
			return "", ErrBusy
		case "request_conflict":
			return "", ErrDuplicate
		}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("post /chat: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("decode /chat response: %w", err)
	}
	return strings.TrimSpace(out.Text), nil
}

// Steer 把提示作为插话转交会话中正在运行的任务；会话空闲时返回错误（应改走 Inject）。
func (i *Injector) Steer(ctx context.Context, sessionID, prompt string) error {
	resp, err := i.post(ctx, "/sessions/"+url.PathEscape(sessionID)+"/steer", map[string]string{
		"prompt": prompt,
	})
	if err != nil {
		return fmt.Errorf("post steer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("post steer: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// ErrRelayUnavailable 表示核心进程内没有运行飞书桥接（或核心版本不支持转交）；
// 调用方应退回旧的「/chat + lark-cli 转达」方式。
var ErrRelayUnavailable = errors.New("feishu relay unavailable")

// RelayRequest 是托付给飞书桥接的一次任务，字段与核心 POST /feishu/relay 一致。
type RelayRequest struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
	RequestID string `json:"request_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Preview   string `json:"preview,omitempty"`
}

// Relay 把任务托付给核心进程内的飞书桥接：由扫码绑定的机器人以流式卡片
// 呈现处理过程与结果。阻塞到任务结束，返回 completed / steered / failed。
func (i *Injector) Relay(ctx context.Context, req RelayRequest) (string, error) {
	resp, err := i.post(ctx, "/feishu/relay", req)
	if err != nil {
		return "", fmt.Errorf("post /feishu/relay: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Status string `json:"status"`
		Code   string `json:"code"`
		Error  string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	switch {
	case resp.StatusCode == http.StatusOK:
		return out.Status, nil
	case resp.StatusCode == http.StatusNotFound || out.Code == "bridge_unavailable":
		return "", ErrRelayUnavailable
	default:
		return "", fmt.Errorf("post /feishu/relay: HTTP %d %s: %s", resp.StatusCode, out.Code, strings.TrimSpace(out.Error))
	}
}
