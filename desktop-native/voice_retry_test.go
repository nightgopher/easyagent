package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestVoiceUploadPreservesAudioFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"voice.wav", pcmWAV([]byte{0, 0})},
		{"voice.m4a", []byte("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				file, header, err := r.FormFile("file")
				if err != nil {
					t.Error(err)
					http.Error(w, "missing file", 400)
					return
				}
				defer file.Close()
				data, err := io.ReadAll(file)
				if err != nil || header.Filename != tc.name || !bytes.Equal(data, tc.data) {
					t.Errorf("wrong audio: filename=%q err=%v", header.Filename, err)
				}
				fmt.Fprint(w, `{"text":"测试"}`)
			}))
			defer server.Close()
			a := newConnectedPreview(t.TempDir())
			defer a.closeLive()
			a.Live.Client, _ = newBackendClient(server.URL, "")
			a.Live.Connected = true
			a.Live.PendingVoice = &pendingVoice{Data: tc.data, Owner: a.current(), Host: a.Live.Config.ActiveHost}
			a.retryVoice()
			waitNative(t, a, func() bool { return !a.Live.Loading["voice"] })
			if a.Live.PendingVoice != nil || a.current().Draft != "测试" {
				t.Fatal("audio was not transcribed")
			}
		})
	}
}

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
