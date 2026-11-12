package feishu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// /chat/stream 返回 409 code=busy 时，streamChat 必须返回 errAgentBusy 哨兵，
// 让上层转插话而不是把 409 当错误抛给用户。
func TestStreamChatBusyReturnsSentinel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":"此会话正在处理消息","code":"busy"}`)
	}))
	defer upstream.Close()
	h := &Handler{piAgentURL: upstream.URL, piAgentAPIKey: "test-key", httpClient: upstream.Client()}
	_, err := h.streamChat(context.Background(), "", "", "test-session", "在吗", nil, "")
	if !errors.Is(err, errAgentBusy) {
		t.Fatalf("expected errAgentBusy, got %v", err)
	}
}

// steer 请求命中 /sessions/{id}/steer、带鉴权与 prompt；200 视为已转交。
func TestSteerEndpointRequestShape(t *testing.T) {
	var gotURI, gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI, gotAuth = r.RequestURI, r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"steered":true}`)
	}))
	defer upstream.Close()
	h := &Handler{piAgentURL: upstream.URL, piAgentAPIKey: "test-key", httpClient: upstream.Client()}
	if !h.steer(context.Background(), "sess-1", "插一句话") {
		t.Fatal("steer should succeed on 200")
	}
	if !strings.Contains(gotURI, "/sessions/sess-1/steer") {
		t.Fatalf("steer hit wrong path: %q", gotURI)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("missing auth header: %q", gotAuth)
	}
	if !strings.Contains(gotBody, "插一句话") {
		t.Fatalf("prompt not forwarded: %q", gotBody)
	}
}

// steer 收到 409 not_running（任务恰好结束）时返回 false。
func TestSteerRejectedWhenIdle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":"agent is not running","code":"not_running"}`)
	}))
	defer upstream.Close()
	h := &Handler{piAgentURL: upstream.URL, piAgentAPIKey: "test-key", httpClient: upstream.Client()}
	if h.steer(context.Background(), "sess-1", "插话") {
		t.Fatal("steer must report failure on not_running")
	}
}

// busy + 插话被接受 → steered=true，不再走普通消息。
func TestStreamChatWithSteerHandsOffWhenAccepted(t *testing.T) {
	var steerCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/stream" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"code":"busy"}`)
			return
		}
		steerCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"steered":true}`)
	}))
	defer upstream.Close()
	h := &Handler{piAgentURL: upstream.URL, piAgentAPIKey: "test-key", httpClient: upstream.Client()}
	text, steered, err := h.streamChatWithSteer(context.Background(), "chat-1", "om_msg", "sess-1", "在吗", nil, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if !steered {
		t.Fatal("expected steered=true when steer endpoint accepts")
	}
	if text != "" {
		t.Fatalf("steered handoff should not carry stream text, got %q", text)
	}
	if steerCalls.Load() != 1 {
		t.Fatalf("steer should be attempted exactly once, got %d", steerCalls.Load())
	}
}

// busy + 插话被拒（任务恰好结束）→ 按普通消息重投一次并取回完整回复。
func TestStreamChatWithSteerFallsBackToResend(t *testing.T) {
	var chatCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/chat/stream":
			if chatCalls.Add(1) == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, `{"code":"busy"}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: done\ndata: {\"final_message\":{\"text\":\"最终回复\"}}\n\n")
		case strings.HasPrefix(r.URL.Path, "/sessions/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"code":"not_running"}`)
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	h := &Handler{piAgentURL: upstream.URL, piAgentAPIKey: "test-key", httpClient: upstream.Client()}
	text, steered, err := h.streamChatWithSteer(context.Background(), "chat-1", "om_msg", "sess-1", "在吗", nil, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if steered {
		t.Fatal("steer must report failure on not_running fallback")
	}
	if text != "最终回复" {
		t.Fatalf("lost reply after resend: %q", text)
	}
	if chatCalls.Load() != 2 {
		t.Fatalf("stream should be retried exactly once, got %d", chatCalls.Load())
	}
}
