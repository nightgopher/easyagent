package wecom

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newQrTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	overrideEndpoints(t, server.URL, server.Client())
	return server
}

func TestCreateQrSession(t *testing.T) {
	var gotQuery map[string]string
	newQrTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != qrGeneratePath {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		gotQuery = map[string]string{"source": q.Get("source"), "plat": q.Get("plat")}
		_, _ = w.Write([]byte(`{"data":{"scode":"sc-1","auth_url":"https://work.weixin.qq.com/ai/qc/gen?u=abc"}}`))
	})
	session, err := CreateQrSession(t.Context())
	if err != nil {
		t.Fatalf("CreateQrSession: %v", err)
	}
	if gotQuery["source"] != qrSource || gotQuery["plat"] != platformCode() {
		t.Fatalf("unexpected generate query: %v", gotQuery)
	}
	if session.Scode != "sc-1" || session.AuthURL != "https://work.weixin.qq.com/ai/qc/gen?u=abc" {
		t.Fatalf("unexpected session: %+v", session)
	}
	if !strings.Contains(session.PageURL, qrPagePath) || !strings.Contains(session.PageURL, "scode=sc-1") {
		t.Fatalf("unexpected page URL: %s", session.PageURL)
	}
}

func TestCreateQrSessionIncomplete(t *testing.T) {
	newQrTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"scode":"sc-1"}}`))
	})
	if _, err := CreateQrSession(t.Context()); err == nil || !strings.Contains(err.Error(), "响应不完整") {
		t.Fatalf("want incomplete error, got %v", err)
	}
}

func TestCreateQrSessionHTTPError(t *testing.T) {
	newQrTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	})
	if _, err := CreateQrSession(t.Context()); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("want HTTP error, got %v", err)
	}
}

func TestWaitSuccess(t *testing.T) {
	var gotScode string
	newQrTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != qrQueryPath {
			http.NotFound(w, r)
			return
		}
		gotScode = r.URL.Query().Get("scode")
		_, _ = w.Write([]byte(`{"data":{"status":"success","bot_info":{"botid":"bot-1","secret":"sec-1"}}}`))
	})
	session := &QrSession{Scode: "sc-9"}
	bot, err := session.Wait(t.Context())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if gotScode != "sc-9" {
		t.Fatalf("poll missing scode: %q", gotScode)
	}
	if bot.BotID != "bot-1" || bot.Secret != "sec-1" {
		t.Fatalf("unexpected bot: %+v", bot)
	}
}

func TestWaitPendingThenSuccess(t *testing.T) {
	overridePoll(t, time.Millisecond, 5*time.Second)
	polls := 0
	newQrTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		polls++
		if polls < 3 {
			_, _ = w.Write([]byte(`{"data":{"status":"waiting_scan"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"status":"success","bot_info":{"botid":"bot-2","secret":"sec-2"}}}`))
	})
	session := &QrSession{Scode: "sc-2"}
	bot, err := session.Wait(t.Context())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if bot.BotID != "bot-2" || polls < 3 {
		t.Fatalf("bot=%+v polls=%d", bot, polls)
	}
}

func TestWaitTimeout(t *testing.T) {
	overridePoll(t, time.Millisecond, 30*time.Millisecond)
	newQrTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"status":"waiting_confirm"}}`))
	})
	session := &QrSession{Scode: "sc-3"}
	if _, err := session.Wait(t.Context()); err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("want timeout error, got %v", err)
	}
}

func TestWaitMissingBotInfo(t *testing.T) {
	newQrTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"status":"success"}}`))
	})
	session := &QrSession{Scode: "sc-4"}
	if _, err := session.Wait(t.Context()); err == nil || !strings.Contains(err.Error(), "机器人凭据") {
		t.Fatalf("want bot info error, got %v", err)
	}
}

func TestWaitContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	newQrTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	cancel()
	session := &QrSession{Scode: "sc-5"}
	if _, err := session.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestPlatformCode(t *testing.T) {
	switch platformCode() {
	case "0", "1", "2", "3":
	default:
		t.Fatalf("unexpected platform code %q", platformCode())
	}
}
