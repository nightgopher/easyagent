package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nightgopher/easyagent/sdk/agent"
)

// ─── POST /v1/chat/completions（OpenAI 兼容）─────────────────────────────────
//
// 让标准 OpenAI 客户端（如 xiaozhi-server 语音后端）把 easyagent 当作对话后端。
// 只取最后一条用户消息作为本轮输入；系统提示词、会话历史与工具
// 仍由 easyagent 会话自身的配置决定，调用方无需（也无法）越权指定。

type openaiMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type openaiChatRequest struct {
	Model    string          `json:"model"`
	Messages []openaiMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	User     string          `json:"user"`
}

type openaiChatText struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content"`
}

type openaiChatChoice struct {
	Index        int             `json:"index"`
	Message      *openaiChatText `json:"message,omitempty"`
	Delta        *openaiChatText `json:"delta,omitempty"`
	FinishReason *string         `json:"finish_reason"`
}

type openaiChatResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []openaiChatChoice `json:"choices"`
}

// content 可能是纯字符串，也可能是 OpenAI 多模态分段数组，取其文本部分。
func (m openaiMessage) text() string {
	switch v := m.Content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, part := range v {
			if obj, ok := part.(map[string]any); ok {
				if t, ok := obj["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func lastUserText(messages []openaiMessage) (string, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			if text := messages[i].text(); text != "" {
				return text, true
			}
		}
	}
	return "", false
}

func (s *Server) registerOpenAIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", s.openaiChatCompletions)
}

func (s *Server) openaiChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req openaiChatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024)).Decode(&req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	prompt, ok := lastUserText(req.Messages)
	if !ok {
		writeError(w, 400, "messages 中没有用户消息")
		return
	}
	sessionID := "openai"
	if req.User != "" {
		sessionID = "openai-" + req.User
	}
	run, _, err := s.startRun(sessionID, prompt, "")
	if err != nil {
		runHTTPError(w, err)
		return
	}
	if req.Stream {
		s.openaiChatStream(w, r, run, req.Model)
		return
	}
	assistant, err := s.waitRun(r.Context(), run)
	if err != nil {
		runHTTPError(w, err)
		return
	}
	stop := "stop"
	writeOpenAIJSON(w, openaiChatResponse{
		ID: "chatcmpl-" + run.ID, Object: "chat.completion", Created: time.Now().Unix(), Model: req.Model,
		Choices: []openaiChatChoice{{Index: 0, Message: &openaiChatText{Role: "assistant", Content: assistant.Text}, FinishReason: &stop}},
	})
}

// openaiChatStream 把会话运行的 text_delta 事件翻译成 OpenAI chunk 流；
// 重放/重复请求的处理与 chatStream 保持一致。
func (s *Server) openaiChatStream(w http.ResponseWriter, r *http.Request, run *sessionRun, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flush := func(data any, done bool) {
		if data != nil {
			buf, _ := json.Marshal(data)
			fmt.Fprintf(w, "data: %s\n\n", buf)
		}
		if done {
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	chunk := func(delta *openaiChatText, stop bool) {
		finish := (*string)(nil)
		if stop {
			reason := "stop"
			finish = &reason
		}
		flush(openaiChatResponse{
			ID: "chatcmpl-" + run.ID, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model,
			Choices: []openaiChatChoice{{Index: 0, Delta: delta, FinishReason: finish}},
		}, false)
	}

	observer := &wsConn{outgoing: make(chan []byte, 512), done: make(chan struct{})}
	defer observer.close()
	defer s.unsubscribeRun(observer, run.SessionID)
	snapshot, err := s.runSnapshot(run.SessionID, 0, "", observer)
	if err != nil {
		writeSSE(w, "error", err.Error())
		return
	}
	if run.restored || snapshot.Run == nil || snapshot.Run.ID != run.ID {
		// 与 chatStream 相同：更晚的命令可能已替换会话投影，老请求仍返回自己的结果。
		assistant, err := s.waitRun(r.Context(), run)
		if err != nil {
			writeSSE(w, "error", err.Error())
			return
		}
		chunk(&openaiChatText{Role: "assistant", Content: assistant.Text}, true)
		return
	}
	emit := func(event agent.AgentStreamEvent) {
		if event.Type == agent.StreamEventTextDelta && event.TextDelta != "" {
			chunk(&openaiChatText{Content: event.TextDelta}, false)
		}
	}
	if events, ok := snapshot.Events.([]agent.AgentStreamEvent); ok {
		for _, event := range events {
			emit(event)
		}
	}
	if snapshot.Run != nil && !runActive(snapshot.Run) {
		flush(nil, true)
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-observer.done:
			return
		case data := <-observer.outgoing:
			var event struct {
				Type  string                 `json:"type"`
				Event agent.AgentStreamEvent `json:"event"`
				State string                 `json:"state"`
			}
			if json.Unmarshal(data, &event) != nil {
				continue
			}
			if event.Type == "event" {
				emit(event.Event)
				if event.Event.Type == agent.StreamEventDone {
					chunk(nil, true)
					return
				}
			}
			if event.Type == "status" && event.State != "running" && event.State != "waiting_confirmation" {
				flush(nil, true)
				return
			}
		}
	}
}

func writeOpenAIJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
