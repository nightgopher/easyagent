package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCustomSpeechRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]string
		if r.Method != "POST" || r.URL.Path != "/v1/audio/speech" || r.Header.Get("Authorization") != "Bearer test-secret" || json.NewDecoder(r.Body).Decode(&v) != nil || v["model"] != "model-a" || v["voice"] != "voice-a" || v["input"] != "你好" || v["response_format"] != "mp3" {
			t.Errorf("unexpected speech request")
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("audio"))
	}))
	defer srv.Close()
	audio, err := customSpeech(context.Background(), srv.URL+"/v1/audio/speech", "model-a", "voice-a", "你好", "test-secret")
	if err != nil || string(audio) != "audio" {
		t.Fatalf("result %q, %v", audio, err)
	}
}
func TestCustomSpeechRejectsUnsafeOrNonAudio(t *testing.T) {
	if _, err := customSpeech(context.Background(), "http://example.com/audio", "m", "v", "hello", "secret"); err == nil {
		t.Fatal("accepted insecure remote endpoint")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"private"}`))
	}))
	defer srv.Close()
	_, err := customSpeech(context.Background(), srv.URL, "m", "v", "hello", "secret")
	if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error %v", err)
	}
}

func TestCustomSpeechDoesNotFollowRedirect(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := customSpeech(context.Background(), source.URL, "m", "v", "hello", "secret")
	if err == nil || called {
		t.Fatalf("redirect followed: called=%v error=%v", called, err)
	}
}
