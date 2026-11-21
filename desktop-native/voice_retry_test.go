package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestVoiceRetryPreservesAudioAndOriginalDraft(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"转写内容"}`)
	}))
	defer server.Close()
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Live.Client, _ = newBackendClient(server.URL, "")
	a.Live.Connected = true
	owner := a.current()
	owner.Draft = "原草稿"
	a.Live.PendingVoice = &pendingVoice{Data: []byte("audio"), Owner: owner, Host: a.Live.Config.ActiveHost}
	a.retryVoice()
	waitNative(t, a, func() bool { return !a.Live.Loading["voice"] })
	if a.Live.PendingVoice == nil || owner.Draft != "原草稿" || a.Live.Error == "" {
		t.Fatal("failed request must retain recording and draft")
	}
	a.retryVoice()
	waitNative(t, a, func() bool { return !a.Live.Loading["voice"] })
	if a.Live.PendingVoice != nil || owner.Draft != "原草稿转写内容" || calls.Load() != 2 {
		t.Fatal("retry should append the transcription exactly once")
	}
}

func TestVoiceRetryDoesNotSendToAnotherHost(t *testing.T) {
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Live.Connected = true
	a.Live.PendingVoice = &pendingVoice{Data: []byte("audio"), Owner: a.current(), Host: "other-host"}
	a.retryVoice()
	if a.Live.PendingVoice == nil || a.Live.Loading["voice"] || a.Live.Error == "" {
		t.Fatal("recording must stay on its original host")
	}
}
