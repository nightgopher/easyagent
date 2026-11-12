package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// tool_end 携带 send_message 时，消息正文必须作为独立聊天消息送达飞书，
// 且不干扰卡片流式收尾。
func TestSendMessageToolEndDeliversStandaloneMessage(t *testing.T) {
	var mu sync.Mutex
	var delivered []string
	feishuAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
		case "/open-apis/im/v1/messages":
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			content, _ := body["content"].(string)
			mu.Lock()
			delivered = append(delivered, content)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"message_id":"om_broadcast_1"}}`)
		default:
			t.Errorf("unexpected feishu API path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer feishuAPI.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: text_delta\ndata: {\"text_delta\":\"正在处理…\"}\n\n")
		fmt.Fprint(w, "event: tool_end\ndata: {\"tool_call_id\":\"c1\",\"tool_name\":\"send_message\",\"tool_result\":\"第一步完成了，继续跑测试\"}\n\n")
		fmt.Fprint(w, "event: text_delta\ndata: {\"text_delta\":\"全部搞定\"}\n\n")
		fmt.Fprint(w, "event: done\ndata: {\"final_message\":{\"text\":\"正在处理…全部搞定\"}}\n\n")
	}))
	defer upstream.Close()

	h := &Handler{
		piAgentURL:    upstream.URL,
		piAgentAPIKey: "test-key",
		httpClient:    upstream.Client(),
		client:        newClientWithBaseURL("app", "secret", feishuAPI.URL),
	}
	text, err := h.streamChat(context.Background(), "chat-1", "om_msg", "sess-1", "task", nil)
	if err != nil {
		t.Fatal(err)
	}
	if text != "正在处理…全部搞定" {
		t.Fatalf("lost stream text: %q", text)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 1 {
		t.Fatalf("expected exactly one standalone broadcast, got %d", len(delivered))
	}
	if !strings.Contains(delivered[0], "第一步完成了，继续跑测试") {
		t.Fatalf("broadcast lost message body: %q", delivered[0])
	}
}

// 空 chatKey 或非 send_message 的 tool_end 不得触发投递；nil client 不崩。
func TestSendMessageToolEndGuards(t *testing.T) {
	h := &Handler{} // client 为 nil

	// nil client：直接调 helper 不崩、不投递。
	h.maybeDeliverBroadcast(context.Background(), "chat-1", "om", `{"tool_name":"send_message","tool_result":"hi"}`)

	// 空 chatKey：streamChat 正常收尾（上游不含 send_message 也不含普通内容）。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: tool_end\ndata: {\"tool_call_id\":\"c1\",\"tool_name\":\"write\",\"tool_result\":\"ok\"}\n\n")
		fmt.Fprint(w, "event: done\ndata: {\"final_message\":{\"text\":\"ok\"}}\n\n")
	}))
	defer upstream.Close()
	h2 := &Handler{piAgentURL: upstream.URL, httpClient: upstream.Client()}
	if _, err := h2.streamChat(context.Background(), "", "", "sess-1", "task", nil); err != nil {
		t.Fatal(err)
	}
}
