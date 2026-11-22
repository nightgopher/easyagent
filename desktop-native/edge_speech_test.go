package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEdgeSpeechRejectsInvalidInput(t *testing.T) {
	if _, err := edgeSpeech(context.Background(), "你好", "invalid voice!"); err == nil {
		t.Fatal("invalid voice accepted")
	}
	if _, err := edgeSpeech(context.Background(), " ", "zh-CN-XiaoxiaoNeural"); err == nil {
		t.Fatal("empty text accepted")
	}
	if _, err := edgeSpeech(context.Background(), strings.Repeat("字", 11000), "zh-CN-XiaoxiaoNeural"); err == nil {
		t.Fatal("oversized text accepted")
	}
}

func TestSpeechTextOmitsLinkDestinationAndFormatting(t *testing.T) {
	got := speechText("## 标题\n请看 [文档](https://example.com/private) 和 **结果**。")
	if got != "标题\n请看 文档 和 结果。" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestEdgeConnectionErrorsDoNotExposeURLs(t *testing.T) {
	got := edgeConnectionError(errors.New("secret-url-and-query"), &http.Response{StatusCode: 403}).Error()
	if strings.Contains(got, "secret-url") || !strings.Contains(got, "403") {
		t.Fatalf("unexpected error %q", got)
	}
}

func TestEdgeSpeechLive(t *testing.T) {
	if os.Getenv("EASYAGENT_TEST_EDGE_TTS") != "1" {
		t.Skip("requires the Edge online service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := edgeSpeech(ctx, "你好，这是语音合成测试。", "zh-CN-XiaoxiaoNeural")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 1000 {
		t.Fatalf("unexpected audio size %d", len(data))
	}
}
