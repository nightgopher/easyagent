package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// relayFeishu records CardKit traffic: created/updated card JSON and the
// receiver of the card message.
type relayFeishu struct {
	mu        sync.Mutex
	cards     []string // 每次创建或更新的卡片 JSON
	receivers []string
	requests  int
}

func (f *relayFeishu) client(t *testing.T) *Client {
	return testCard(func(r *http.Request) (*http.Response, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		switch {
		case strings.Contains(r.URL.Path, "/cardkit/v1/cards"):
			var body struct {
				Data string `json:"data"`
				Card struct {
					Data string `json:"data"`
				} `json:"card"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			if body.Data == "" {
				body.Data = body.Card.Data
			}
			f.cards = append(f.cards, body.Data)
			return cardResponse(`{"code":0,"data":{"card_id":"card-relay"}}`), nil
		case strings.Contains(r.URL.Path, "/im/v1/messages"):
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			f.receivers = append(f.receivers, fmt.Sprint(body["receive_id"]))
			return cardResponse(`{"code":0,"data":{"message_id":"om_relay"}}`), nil
		}
		return nil, fmt.Errorf("unexpected Feishu request %s", r.URL)
	}).client
}

func (f *relayFeishu) snapshot() (cards, receivers []string, requests int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cards...), append([]string(nil), f.receivers...), f.requests
}

func relayHandler(t *testing.T, core *httptest.Server, f *relayFeishu) *Handler {
	t.Helper()
	owner, err := OpenOwnerAccess(filepath.Join(t.TempDir(), "owner.json"), "ou_owner")
	require.NoError(t, err)
	return &Handler{access: owner, client: f.client(t), piAgentURL: core.URL, httpClient: core.Client(), routes: map[string]*ChatRoute{}}
}

func writeEvent(w http.ResponseWriter, event, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// TestRelayRendersHeaderedCardInOwnerChat：托付任务以 larkwatch 渠道提交，
// 卡片带看门狗标题栏，正文先是新消息摘要、再是回复，发到会话绑定的私聊。
func TestRelayRendersHeaderedCardInOwnerChat(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/chat/stream", r.URL.Path)
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "larkwatch", body["channel"])
		require.Equal(t, "session-butler", body["session_id"])
		require.Equal(t, "larkwatch-abc", body["request_id"])
		w.Header().Set("Content-Type", "text/event-stream")
		writeEvent(w, "accepted", `{"session_id":"session-butler","run_id":"run-1"}`)
		writeEvent(w, "text_delta", `{"text_delta":"已回复张三：明天见。"}`)
		writeEvent(w, "done", `{"final_message":{"text":"已回复张三：明天见。"}}`)
	}))
	defer core.Close()
	f := &relayFeishu{}
	h := relayHandler(t, core, f)
	h.routes["ou_owner"] = &ChatRoute{SessionID: "session-butler"}

	result, err := h.Relay(context.Background(), RelayRequest{
		SessionID: "session-butler", Prompt: "【飞书看门狗】...", RequestID: "larkwatch-abc",
		Title: "🐕 看门狗 · 1 条新消息", Preview: "**张三**　10:00\n明天开会吗",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Contains(t, result.Text, "明天见")

	cards, receivers, _ := f.snapshot()
	require.Equal(t, []string{"ou_owner"}, receivers)
	require.NotEmpty(t, cards)
	final := cards[len(cards)-1]
	require.Contains(t, final, "🐕 看门狗 · 1 条新消息")
	require.Contains(t, final, `"template":"turquoise"`)
	require.Contains(t, final, "明天开会吗")
	require.Contains(t, final, "明天见")
	require.Less(t, strings.Index(final, "明天开会吗"), strings.Index(final, "明天见"), "preview must precede the reply")
}

// TestRelayNotAdmittedSendsNoCard：核心拒绝受理时不发卡片，交给调用方重试。
func TestRelayNotAdmittedSendsNoCard(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
	}))
	defer core.Close()
	f := &relayFeishu{}
	h := relayHandler(t, core, f)

	_, err := h.Relay(context.Background(), RelayRequest{SessionID: "missing", Prompt: "hi", Title: "🐕"})
	require.True(t, errors.Is(err, ErrRelayNotAdmitted), "got %v", err)
	_, _, requests := f.snapshot()
	require.Zero(t, requests, "no Feishu traffic when nothing ran")
}

// TestRelayBusySessionSteers：会话忙时转插话，卡片说明已插话。
func TestRelayBusySessionSteers(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/stream":
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"code":"busy"}`)
		case "/sessions/session-butler/steer":
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected core request %s", r.URL.Path)
		}
	}))
	defer core.Close()
	f := &relayFeishu{}
	h := relayHandler(t, core, f)

	result, err := h.Relay(context.Background(), RelayRequest{SessionID: "session-butler", Prompt: "hi", Title: "🐕 看门狗", Preview: "**李四**　11:00\n在吗"})
	require.NoError(t, err)
	require.Equal(t, "steered", result.Status)
	cards, receivers, _ := f.snapshot()
	require.Equal(t, []string{"ou_owner"}, receivers, "unbound session falls back to the owner DM")
	require.Len(t, cards, 1)
	require.Contains(t, cards[0], "在吗")
	require.Contains(t, cards[0], "已插话")
}

// TestStartRelayReturnsOnceAdmitted：受理即返回，卡片在后台继续更新到任务结束。
func TestStartRelayReturnsOnceAdmitted(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		w.Header().Set("Content-Type", "text/event-stream")
		writeEvent(w, "accepted", `{"session_id":"session-butler","run_id":"run-1"}`)
		<-release
		writeEvent(w, "done", `{"final_message":{"text":"处理完毕"}}`)
	}))
	defer core.Close()
	f := &relayFeishu{}
	h := relayHandler(t, core, f)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wait, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	result, err := h.StartRelay(ctx, wait, RelayRequest{SessionID: "session-butler", Prompt: "hi", Title: "🐕"})
	require.NoError(t, err)
	require.Equal(t, "accepted", result.Status)

	close(release)
	<-finished
	require.Eventually(t, func() bool {
		cards, _, _ := f.snapshot()
		return len(cards) > 0 && strings.Contains(cards[len(cards)-1], "处理完毕")
	}, 2*time.Second, 10*time.Millisecond)
}
