package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBroadcastOutboxRetriesAfterRestartWithSameIdentity(t *testing.T) {
	var mu sync.Mutex
	var ids []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test","expire":7200}`)
			return
		}
		if r.URL.Path != "/open-apis/im/v1/messages" {
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		var body struct {
			UUID string `json:"uuid"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		ids = append(ids, body.UUID)
		n := len(ids)
		mu.Unlock()
		if n == 1 {
			fmt.Fprint(w, `{"code":99999,"msg":"temporary failure"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"message_id":"delivered"}}`)
	}))
	defer api.Close()
	path := filepath.Join(t.TempDir(), "outbox.sqlite")
	makeHandler := func() *Handler {
		return &Handler{appID: "app", client: newClientWithBaseURL("app", "secret", api.URL), outbox: &notificationOutbox{path: path, appID: "app"}, outboxWake: make(chan struct{}, 1)}
	}
	ctx := context.Background()
	h := makeHandler()
	require.NoError(t, h.enqueueBroadcast(ctx, "chat", "incoming", "call-1", "important"))
	require.NoError(t, h.enqueueBroadcast(ctx, "chat", "incoming", "call-1", "important"))
	h.deliverNotifications(ctx)
	db, err := h.outbox.open(ctx)
	require.NoError(t, err)
	var state string
	var attempts int
	require.NoError(t, db.QueryRow(`SELECT state,attempts FROM notifications`).Scan(&state, &attempts))
	require.Equal(t, "pending", state)
	require.Equal(t, 1, attempts)
	_, err = db.Exec(`UPDATE notifications SET next_ms=0`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	h = makeHandler()
	h.deliverNotifications(ctx)
	db, err = h.outbox.open(ctx)
	require.NoError(t, err)
	defer db.Close()
	var body string
	require.NoError(t, db.QueryRow(`SELECT state,body FROM notifications`).Scan(&state, &body))
	require.Equal(t, "sent", state)
	require.Empty(t, body)
	h.deliverNotifications(ctx)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, ids, 2)
	require.NotEmpty(t, ids[0])
	require.Equal(t, ids[0], ids[1])
}

func TestBroadcastOutboxDoesNotRetryPastDedupWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.sqlite")
	h := &Handler{appID: "app", client: newClientWithBaseURL("app", "secret", "http://127.0.0.1:1"), outbox: &notificationOutbox{path: path, appID: "app"}}
	ctx := context.Background()
	require.NoError(t, h.enqueueBroadcast(ctx, "chat", "incoming", "call", "message"))
	db, err := h.outbox.open(ctx)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE notifications SET created_ms=0, next_ms=0`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	h.deliverNotifications(ctx)
	db, err = h.outbox.open(ctx)
	require.NoError(t, err)
	defer db.Close()
	var state string
	var attempts int
	require.NoError(t, db.QueryRow(`SELECT state,attempts FROM notifications`).Scan(&state, &attempts))
	require.Equal(t, "failed", state)
	require.Zero(t, attempts)
}

func TestBroadcastEventQueuesAndWorkerDelivers(t *testing.T) {
	delivered := make(chan string, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"test","expire":7200}`)
			return
		}
		if r.URL.Path != "/open-apis/im/v1/messages" {
			w.WriteHeader(404)
			return
		}
		var body struct {
			UUID string `json:"uuid"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		delivered <- body.UUID
		fmt.Fprint(w, `{"code":0,"data":{"message_id":"delivered"}}`)
	}))
	defer api.Close()
	h := &Handler{appID: "app", client: newClientWithBaseURL("app", "secret", api.URL), outbox: &notificationOutbox{path: filepath.Join(t.TempDir(), "outbox.sqlite"), appID: "app"}, outboxWake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.StartNotificationWorker(ctx)
	h.maybeDeliverBroadcast(ctx, "chat", "incoming", `{"tool_name":"send_message","tool_call_id":"call","tool_result":"important"}`)
	select {
	case id := <-delivered:
		require.Equal(t, notificationID("app", "chat", "incoming", "call"), id)
	case <-time.After(3 * time.Second):
		t.Fatal("notification was not delivered")
	}
	require.Eventually(t, func() bool {
		db, err := h.outbox.open(context.Background())
		if err != nil {
			return false
		}
		defer db.Close()
		var state string
		return db.QueryRow(`SELECT state FROM notifications`).Scan(&state) == nil && state == "sent"
	}, 3*time.Second, 10*time.Millisecond)
}
