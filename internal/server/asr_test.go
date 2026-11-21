package server

import (
	"bytes"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/config"
)

func TestASRGatewayEndpoint(t *testing.T) {
	for _, base := range []string{"", "/v1", "/v1/", "/gateway/v1"} {
		t.Run(base, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := "/v1/audio/transcriptions"
				if base == "/gateway/v1" {
					want = "/gateway/v1/audio/transcriptions"
				}
				if r.URL.Path != want || r.Header.Get("Authorization") != "Bearer qa-only" {
					t.Error("wrong endpoint/credential", r.URL.Path)
				}
				if err := r.ParseMultipartForm(1024); err != nil {
					t.Error(err)
				}
				defer func() { _ = r.MultipartForm.RemoveAll() }()
				if r.FormValue("model") != "qa-asr" {
					t.Error("lost model")
				}
				_, _ = io.WriteString(w, `{"text":"recognized"}`)
			}))
			defer upstream.Close()
			var b bytes.Buffer
			m := multipart.NewWriter(&b)
			part, _ := m.CreateFormFile("file", "qa.webm")
			if _, err := part.Write([]byte("test audio")); err != nil {
				t.Fatal(err)
			}
			m.Close()
			r := httptest.NewRequest("POST", "/asr/transcribe", &b)
			r.Header.Set("Content-Type", m.FormDataContentType())
			w := httptest.NewRecorder()
			NewASRHandler(config.Config{ASRAPIKey: "qa-only", ASRBaseURL: upstream.URL + base, ASRModel: "qa-asr"}).transcribe(w, r)
			if w.Code != 200 || w.Body.String() != "{\"text\":\"recognized\"}\n" {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestASRDoesNotLogTranscriptOrUpstreamBody(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"text":"private spoken words","message":"private spoken words"}`)
		}))
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", "voice.wav")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("audio"))
		_ = form.Close()
		req := httptest.NewRequest("POST", "/asr/transcribe", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		response := httptest.NewRecorder()
		NewASRHandler(config.Config{ASRAPIKey: "qa-only", ASRBaseURL: upstream.URL}).transcribe(response, req)
		upstream.Close()
		if response.Code != status {
			t.Fatalf("status = %d, want %d", response.Code, status)
		}
	}
	if strings.Contains(logs.String(), "private spoken words") {
		t.Fatal("ASR logged sensitive speech or upstream response")
	}
}

func TestASRRejectsOversizedUpload(t *testing.T) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "voice.wav")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(bytes.Repeat([]byte{'a'}, 25<<20))
	_ = form.Close()
	req := httptest.NewRequest("POST", "/asr/transcribe", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	NewASRHandler(config.Config{ASRAPIKey: "qa-only"}).transcribe(response, req)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload: got %d", response.Code)
	}
}

func TestASRCleansMultipartTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"text":"ok"}`)
	}))
	defer upstream.Close()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "voice.wav")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(bytes.Repeat([]byte{'a'}, 9<<20))
	_ = form.Close()
	req := httptest.NewRequest("POST", "/asr/transcribe", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	NewASRHandler(config.Config{ASRAPIKey: "qa-only", ASRBaseURL: upstream.URL}).transcribe(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary uploads remain: %v %v", files, err)
	}
}
