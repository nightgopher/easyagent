package feishu

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamChatShowsModelRetryWhileWaiting(t *testing.T) {
	waiting := make(chan struct{}, 1)
	card := testCard(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "正在自动重试（1/3，本次等待 10 秒）") {
			if !strings.Contains(string(body), "已完成的操作会保留") || !strings.Contains(string(body), "文件已保存") {
				t.Error("retry lost existing progress")
			}
			select {
			case waiting <- struct{}{}:
			default:
			}
		}
		return cardResponse(`{"code":0}`), nil
	})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: text_delta\ndata: {\"text_delta\":\"文件已保存\"}\n\n")
		fmt.Fprint(w, "event: model_retry\ndata: {\"retry\":{\"attempt\":1,\"max_attempts\":3,\"delay_ms\":10000,\"reason\":\"rate_limit\"}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-waiting:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "event: text_delta\ndata: {\"text_delta\":\"，任务完成\"}\n\nevent: done\ndata: {}\n\n")
	}))
	defer upstream.Close()
	h := &Handler{piAgentURL: upstream.URL, httpClient: upstream.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	text, err := h.streamChat(ctx, "", "", "retry-session", "finish", card)
	if err != nil {
		t.Fatal(err)
	}
	if text != "文件已保存，任务完成" {
		t.Fatal(text)
	}
}
