package larkwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLark 按 lark-cli 参数返回预设输出，并记录发送调用。测试是单 goroutine 串行调用。
type fakeLark struct {
	handler func(args []string) (string, error)
	sent    []string // 记录 +messages-send 的 (chatID|identity|text)
}

func (f *fakeLark) Output(_ context.Context, args ...string) ([]byte, error) {
	if f.handler == nil {
		return nil, fmt.Errorf("no handler")
	}
	out, err := f.handler(args)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// sentMessages 返回已记录的 (chatID|identity|text) 发送记录。
func (f *fakeLark) sentMessages() []string {
	return append([]string(nil), f.sent...)
}

const (
	testSelfID   = "ou_owner"
	testBotID    = "ou_bot"
	testBotChat  = "oc_botchat"
	testPeerChat = "oc_peer"
	testSession  = "sess_butler"
)

func msgJSON(id string, pos int, timeStr, senderID, senderName, content string) string {
	return fmt.Sprintf(`{"message_id":%q,"message_position":"%d","create_time":%q,"msg_type":"text","content":%q,"deleted":false,"sender":{"id":%q,"name":%q}}`,
		id, pos, timeStr, content, senderID, senderName)
}

func chatsJSON(extra ...string) string {
	chats := []string{
		fmt.Sprintf(`{"chat_id":%q,"name":"管家","p2p_target_id":%q,"p2p_target_type":"bot"}`, testBotChat, testBotID),
		fmt.Sprintf(`{"chat_id":%q,"name":"王伟","p2p_target_id":"ou_peer","p2p_target_type":"user"}`, testPeerChat),
	}
	chats = append(chats, extra...)
	return fmt.Sprintf(`{"chats":[%s],"has_more":false}`, strings.Join(chats, ","))
}

func messagesJSON(items ...string) string {
	return fmt.Sprintf(`{"messages":[%s]}`, strings.Join(items, ","))
}

func indexOf(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

// serverRecorder 记录看门狗打到核心服务的请求。
type serverRecorder struct {
	mu    sync.Mutex
	posts []posted
}

type posted struct {
	path string
	body map[string]string
}

func (r *serverRecorder) record(path string, body map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posts = append(r.posts, posted{path: path, body: body})
}

func (r *serverRecorder) byPath(path string) []posted {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []posted
	for _, p := range r.posts {
		if p.path == path {
			out = append(out, p)
		}
	}
	return out
}

// newTestWatch 构造看门狗与配套 httptest 核心服务。chatStatus 决定 /chat 的响应。
func newTestWatch(t *testing.T, lark *fakeLark, chatStatus func(path string, body map[string]string) (int, string)) (*Watch, *serverRecorder) {
	t.Helper()
	rec := &serverRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.record("chat", body)
		code, text := chatStatus("/chat", body)
		w.Header().Set("Content-Type", "application/json")
		if code == http.StatusConflict {
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"code":%q}`, text)
			return
		}
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"text":%q,"session_id":%q}`, text, body["session_id"])
	})
	mux.HandleFunc("POST /sessions/"+testSession+"/steer", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.record("steer", body)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	injector := &Injector{BaseURL: server.URL, APIKey: "test-key"}
	watch, err := New(
		Config{CoreURL: server.URL, APIKey: "test-key", SessionID: testSession, Interval: time.Second},
		lark, injector, "", slog.New(slog.NewTextHandler(&strings.Builder{}, nil)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return watch, rec
}

// standardLark 返回一个常规假 lark-cli：主人+王伟单聊，王伟有一条 latestPosition 位的新消息。
func standardLark(latestPeerMessage string, latestPosition int) *fakeLark {
	lark := &fakeLark{}
	lark.handler = func(args []string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "contact +get-user"):
			return `{"ok":true,"data":{"user":{"open_id":"` + testSelfID + `"}}}`, nil
		case strings.HasPrefix(joined, "auth status"):
			return `{"identities":{"bot":{"openId":"` + testBotID + `"}}}`, nil
		case strings.HasPrefix(joined, "im +chat-list"):
			return `{"ok":true,"data":` + chatsJSON() + `}`, nil
		case strings.HasPrefix(joined, "im +chat-messages-list") && strings.Contains(joined, testPeerChat):
			return `{"ok":true,"data":` + messagesJSON(latestPeerMessage) + `}`, nil
		case strings.HasPrefix(joined, "im +chat-messages-list"):
			return `{"ok":true,"data":` + messagesJSON() + `}`, nil
		case strings.HasPrefix(joined, "im +messages-send"):
			chatID := args[indexOf(args, "--chat-id")+1]
			text := args[indexOf(args, "--text")+1]
			identity := args[indexOf(args, "--as")+1]
			lark.sent = append(lark.sent, chatID+"|"+identity+"|"+text)
			return `{"ok":true,"data":{"message_id":"om_out"}}`, nil
		}
		return "", fmt.Errorf("unexpected lark args: %s", joined)
	}
	return lark
}

// rewind 把会话位点回拨，模拟「上一轮已处理到 position」。
func rewind(watch *Watch, chatID string, position int64) {
	watch.state.Chats[chatID] = ChatState{LastPosition: position}
}

func TestFirstRunBaselinesWithoutTriggering(t *testing.T) {
	lark := standardLark(msgJSON("om_old", 10, "2026-11-11 10:00", "ou_peer", "王伟", "早"), 10)
	watch, rec := newTestWatch(t, lark, func(string, map[string]string) (int, string) {
		return http.StatusOK, "ok"
	})
	ctx := context.Background()
	if err := watch.init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := watch.Poll(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := len(rec.byPath("chat")); got != 0 {
		t.Fatalf("baseline should not wake session, got %d posts", got)
	}
	if got := watch.state.Position(testPeerChat); got != 10 {
		t.Fatalf("baseline position = %d, want 10", got)
	}
}

func TestPollWakesSessionAndRelays(t *testing.T) {
	lark := standardLark(msgJSON("om_new", 11, "2026-11-12 09:30", "ou_peer", "王伟", "下午有空吗"), 11)
	watch, rec := newTestWatch(t, lark, func(string, map[string]string) (int, string) {
		return http.StatusOK, "已经回复王伟：有空"
	})
	if err := watch.init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	// init 基线到 11；回拨模拟「上一轮只处理到 10」，这样位置 11 是新消息。
	rewind(watch, testPeerChat, 10)

	if err := watch.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	posts := rec.byPath("chat")
	if len(posts) != 1 {
		t.Fatalf("expected 1 wake, got %d", len(posts))
	}
	body := posts[0].body
	if body["session_id"] != testSession {
		t.Errorf("session_id = %q", body["session_id"])
	}
	if body["channel"] != "larkwatch" {
		t.Errorf("channel = %q", body["channel"])
	}
	if !strings.HasPrefix(body["request_id"], "larkwatch-") {
		t.Errorf("request_id = %q", body["request_id"])
	}
	if !strings.Contains(body["prompt"], "王伟") || !strings.Contains(body["prompt"], "下午有空吗") {
		t.Errorf("prompt missing message info: %q", body["prompt"])
	}
	if got := watch.state.Position(testPeerChat); got != 11 {
		t.Errorf("position after wake = %d, want 11", got)
	}
	sent := lark.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 relay message, got %d", len(sent))
	}
	if !strings.HasPrefix(sent[0], testBotChat+"|bot|") {
		t.Errorf("relay = %q, want bot chat relay", sent[0])
	}
	if !strings.Contains(sent[0], "已经回复王伟") {
		t.Errorf("relay missing agent text: %q", sent[0])
	}
}

func TestPollSkipsSelfMessages(t *testing.T) {
	lark := standardLark(msgJSON("om_self", 12, "2026-11-12 09:35", testSelfID, "我", "刚发出去的"), 12)
	watch, rec := newTestWatch(t, lark, func(string, map[string]string) (int, string) {
		return http.StatusOK, ""
	})
	ctx := context.Background()
	if err := watch.init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	rewind(watch, testPeerChat, 10)
	if err := watch.Poll(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := len(rec.byPath("chat")); got != 0 {
		t.Fatalf("self message should not wake, got %d wakes", got)
	}
	if got := watch.state.Position(testPeerChat); got != 12 {
		t.Errorf("position = %d, want 12（自消息也要推进位点）", got)
	}
}

func TestPollExcludesBotAndExplicitChats(t *testing.T) {
	extra := `{"chat_id":"oc_muted","name":"已忽略","p2p_target_id":"ou_x","p2p_target_type":"user"}`
	lark := &fakeLark{}
	lark.handler = func(args []string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "contact +get-user"):
			return `{"ok":true,"data":{"user":{"open_id":"` + testSelfID + `"}}}`, nil
		case strings.HasPrefix(joined, "auth status"):
			return `{"identities":{"bot":{"openId":"` + testBotID + `"}}}`, nil
		case strings.HasPrefix(joined, "im +chat-list"):
			return `{"ok":true,"data":` + chatsJSON(extra) + `}`, nil
		case strings.HasPrefix(joined, "im +chat-messages-list") && strings.Contains(joined, "oc_muted"):
			return `{"ok":true,"data":` + messagesJSON(msgJSON("om_m", 5, "2026-11-12 09:00", "ou_x", "某人", "打扰了")) + `}`, nil
		case strings.HasPrefix(joined, "im +chat-messages-list"):
			return `{"ok":true,"data":` + messagesJSON() + `}`, nil
		case strings.HasPrefix(joined, "im +messages-send"):
			return `{"ok":true,"data":{}}`, nil
		}
		return "", fmt.Errorf("unexpected lark args: %s", joined)
	}
	watch, rec := newTestWatch(t, lark, func(string, map[string]string) (int, string) {
		return http.StatusOK, ""
	})
	watch.cfg.ExcludeChats = map[string]bool{"oc_muted": true}
	ctx := context.Background()
	if err := watch.init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := watch.Poll(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := len(rec.byPath("chat")); got != 0 {
		t.Fatalf("excluded chats should not wake, got %d", got)
	}
	if _, watched := watch.state.Chats["oc_muted"]; watched {
		t.Errorf("excluded chat should not enter state")
	}
}

func TestBusyFallsBackToSteer(t *testing.T) {
	lark := standardLark(msgJSON("om_n", 11, "2026-11-12 09:30", "ou_peer", "王伟", "在吗"), 11)
	watch, rec := newTestWatch(t, lark, func(path string, _ map[string]string) (int, string) {
		if path == "/chat" {
			return http.StatusConflict, "busy"
		}
		return http.StatusOK, ""
	})
	if err := watch.init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	rewind(watch, testPeerChat, 10)
	if err := watch.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := len(rec.byPath("steer")); got != 1 {
		t.Fatalf("expected 1 steer, got %d", got)
	}
	if got := watch.state.Position(testPeerChat); got != 11 {
		t.Errorf("position = %d, want 11", got)
	}
}

func TestDuplicateRequestAdvancesWithoutRelay(t *testing.T) {
	lark := standardLark(msgJSON("om_n", 11, "2026-11-12 09:30", "ou_peer", "王伟", "在吗"), 11)
	watch, _ := newTestWatch(t, lark, func(path string, _ map[string]string) (int, string) {
		if path == "/chat" {
			return http.StatusConflict, "request_conflict"
		}
		return http.StatusOK, ""
	})
	if err := watch.init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	rewind(watch, testPeerChat, 10)
	if err := watch.Poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := watch.state.Position(testPeerChat); got != 11 {
		t.Errorf("position = %d, want 11", got)
	}
	if sent := lark.sentMessages(); len(sent) != 0 {
		t.Errorf("duplicate should not relay, got %v", sent)
	}
}

func TestPollFailureKeepsPositionForRetry(t *testing.T) {
	lark := standardLark(msgJSON("om_n", 11, "2026-11-12 09:30", "ou_peer", "王伟", "在吗"), 11)
	watch, _ := newTestWatch(t, lark, func(path string, _ map[string]string) (int, string) {
		if path == "/chat" {
			return http.StatusInternalServerError, "boom"
		}
		return http.StatusOK, ""
	})
	if err := watch.init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	rewind(watch, testPeerChat, 10)
	if err := watch.Poll(context.Background()); err == nil {
		t.Fatal("expected error from failing core")
	}
	if got := watch.state.Position(testPeerChat); got != 10 {
		t.Errorf("position = %d, want 10（失败不推进，下轮重试）", got)
	}
}

func TestRequestIDStableAcrossPolls(t *testing.T) {
	items := []sighting{{
		chat: P2PChat{ChatID: testPeerChat},
		msgs: []Message{{ID: "om_1", Position: 11, Time: "2026-11-12 09:30"}},
	}}
	first := requestID(items)
	if again := requestID(items); first != again {
		t.Fatalf("request id not stable: %q vs %q", first, again)
	}
	items[0].msgs[0].Position = 12
	if requestID(items) == first {
		t.Fatal("different messages should produce different request id")
	}
}

func TestBuildPrompt(t *testing.T) {
	items := []sighting{{
		chat: P2PChat{ChatID: testPeerChat, Name: "王伟"},
		msgs: []Message{
			{ID: "om_1", Position: 11, Time: "2026-11-12 09:30", SenderID: "ou_peer", Sender: "王伟", IsText: true, Content: "在吗"},
			{ID: "om_2", Position: 12, Time: "2026-11-12 09:31", SenderID: "ou_peer", Sender: "王伟", IsText: false},
		},
	}}
	got := buildPrompt(items, 2)
	for _, want := range []string{"发现 2 条新消息", "王伟（2026-11-12 09:30，来自会话「王伟」）：在吗", "butler-rules", "[unknown 消息，无文字内容]"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	long := strings.Repeat("汉", maxRelayRunes+10)
	got := truncateRunes(long, maxRelayRunes)
	if got == long {
		t.Fatal("expected truncation")
	}
	if !strings.HasSuffix(got, "…（截断）") {
		t.Errorf("truncated text should carry marker")
	}
	if short := truncateRunes("短消息", 10); short != "短消息" {
		t.Errorf("short text should stay as is, got %q", short)
	}
}

func TestCapSightingsKeepsNewest(t *testing.T) {
	sightings := []sighting{
		{chat: P2PChat{ChatID: "oc_a", Name: "甲"}, msgs: []Message{
			{ID: "a1", Time: "2026-11-12 08:00", Sender: "甲", IsText: true, Content: "older"},
			{ID: "a2", Time: "2026-11-12 10:00", Sender: "甲", IsText: true, Content: "newer"},
		}},
		{chat: P2PChat{ChatID: "oc_b", Name: "乙"}, msgs: []Message{
			{ID: "b1", Time: "2026-11-12 12:00", Sender: "乙", IsText: true, Content: "newest"},
		}},
	}
	got, shown := capSightings(sightings, 2)
	if shown != 2 {
		t.Fatalf("shown = %d, want 2", shown)
	}
	if len(got) != 2 {
		t.Fatalf("groups = %d, want 2", len(got))
	}
	var contents []string
	for _, s := range got {
		for _, m := range s.msgs {
			contents = append(contents, m.Content)
		}
	}
	// 组间按最新消息时间新→旧（乙 12:00 在前），组内按时间正序。
	if len(contents) != 2 || contents[0] != "newest" || contents[1] != "newer" {
		t.Errorf("kept = %v, want [newest newer]（组间新→旧、组内旧→新）", contents)
	}
}

func TestConfigValidate(t *testing.T) {
	cfg := Config{}
	if err := cfg.Validate(); err == nil {
		t.Fatal("empty config should fail validation")
	}
	cfg = Config{SessionID: "s", CoreURL: "http://x", Interval: time.Second, MaxPerWake: -1}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config failed: %v", err)
	}
	if cfg.Interval != MinInterval {
		t.Errorf("interval = %v, want clamped to %v", cfg.Interval, MinInterval)
	}
	if cfg.MaxPerWake != DefaultMaxPerWake {
		t.Errorf("maxPerWake = %d, want default", cfg.MaxPerWake)
	}
}

func TestBuildPromptCarriesRichTextContentAndChatName(t *testing.T) {
	items := []sighting{{
		chat: P2PChat{ChatID: "oc_x", Name: "韦贺文"},
		msgs: []Message{
			{Sender: "韦贺文", Time: "01:05", IsText: false, MsgType: "post", Content: "赛豆额度治理 PRD 推进建议"},
			{Sender: "韦贺文", Time: "01:06", IsText: false, MsgType: "image", Content: ""},
		},
	}}
	out := buildPrompt(items, 2)
	if !strings.Contains(out, "赛豆额度治理 PRD 推进建议") {
		t.Fatalf("rich text content lost:\n%s", out)
	}
	if strings.Contains(out, "[非文本消息]") {
		t.Fatalf("legacy placeholder leaked:\n%s", out)
	}
	if !strings.Contains(out, "[image 消息，无文字内容]") {
		t.Fatalf("type label missing:\n%s", out)
	}
	if !strings.Contains(out, "来自会话「韦贺文」") {
		t.Fatalf("chat name missing:\n%s", out)
	}
}
