package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionBucketsUseCoreActivityMetadata(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	old := now.AddDate(0, 0, -10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions":
			// server.SessionResponse uses Unix seconds, not updated_at.
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "created", "created_at": now.Unix()})
		case r.URL.Path == "/sessions":
			// sessionmgr.SessionInfo supplies activity time as last_active.
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "created", "created_at": now.Unix(), "last_active": old.Unix()}})
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Live.Client, _ = newBackendClient(server.URL, "")
	a.Live.Connected = true
	if sessionBucket(a.current().UpdatedAt) != 0 {
		t.Fatal("the initial draft session must appear under today")
	}
	a.createLiveSession("")
	waitNative(t, a, func() bool { return a.current().RemoteID != "" && !a.Live.Loading["create-session"] })
	s := a.current()
	if sessionBucket(s.UpdatedAt) != 0 {
		t.Fatalf("new session must appear under today before refresh, timestamp = %q", s.UpdatedAt)
	}
	a.refreshLiveSessions(s.RemoteID)
	waitNative(t, a, func() bool { return s.UpdatedAt != "" && sessionBucket(s.UpdatedAt) == 2 })
	stamp, err := time.Parse(time.RFC3339, s.UpdatedAt)
	if err != nil || stamp.Unix() != old.Unix() {
		t.Fatalf("core last_active was not preserved: %q, %v", s.UpdatedAt, err)
	}
}

func TestSessionBucketGroupsByLocalCalendarAge(t *testing.T) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	cases := []struct {
		name string
		at   time.Time
		want int
	}{
		{"now", now, 0},
		{"start of today", today, 0},
		{"yesterday evening", today.Add(-time.Hour), 1},
		{"six days ago", today.AddDate(0, 0, -6), 1},
		{"seven days ago", today.AddDate(0, 0, -7), 2},
		{"last month", today.AddDate(0, -1, 0), 2},
	}
	for _, tc := range cases {
		if got := sessionBucket(tc.at.Format(time.RFC3339)); got != tc.want {
			t.Errorf("%s: bucket = %d, want %d", tc.name, got, tc.want)
		}
	}
	if got := sessionBucket(fmt.Sprint(today.Unix())); got != 2 {
		t.Errorf("non-RFC3339 stamp should sort into the oldest bucket, got %d", got)
	}
}
