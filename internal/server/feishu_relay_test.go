package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/internal/feishu"
)

func relayRequest(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	s.feishuRelay(w, httptest.NewRequest(http.MethodPost, "/feishu/relay", strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// 桥接不在本进程运行时返回 bridge_unavailable，看门狗据此退回文本转达。
func TestFeishuRelayWithoutBridge(t *testing.T) {
	code, out := relayRequest(t, &Server{}, `{"session_id":"s","prompt":"p"}`)
	if code != http.StatusServiceUnavailable || out["code"] != "bridge_unavailable" {
		t.Fatalf("want 503 bridge_unavailable, got %d %v", code, out)
	}
}

// 入口随桥接代的生命周期失效；结果与错误映射到对应的状态码。
func TestFeishuRelayRoutesToLiveBridge(t *testing.T) {
	s := &Server{}
	ctx, cancel := context.WithCancel(context.Background())
	var got feishu.RelayRequest
	var fail error
	s.localBridge.mu.Lock()
	s.localBridge.relay = func(_ context.Context, req feishu.RelayRequest) (feishu.RelayResult, error) {
		got = req
		return feishu.RelayResult{Status: "accepted"}, fail
	}
	s.localBridge.relayCtx = ctx
	s.localBridge.mu.Unlock()

	code, out := relayRequest(t, s, `{"session_id":"sess","prompt":"hi","title":"🐕","preview":"p"}`)
	if code != http.StatusOK || out["status"] != "accepted" || got.SessionID != "sess" || got.Title != "🐕" {
		t.Fatalf("relay not forwarded: %d %v %+v", code, out, got)
	}

	fail = fmt.Errorf("%w: agent stream HTTP 404", feishu.ErrRelayNotAdmitted)
	if code, out := relayRequest(t, s, `{"session_id":"sess","prompt":"hi"}`); code != http.StatusConflict || out["code"] != "not_admitted" {
		t.Fatalf("want 409 not_admitted, got %d %v", code, out)
	}

	cancel()
	if code, out := relayRequest(t, s, `{"session_id":"sess","prompt":"hi"}`); code != http.StatusServiceUnavailable || out["code"] != "bridge_unavailable" {
		t.Fatalf("ended bridge generation must not accept relays, got %d %v", code, out)
	}
}
