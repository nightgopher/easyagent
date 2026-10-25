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
//
// 函数调用桥接：调用方传入的 tools 会以协议形式注入提示词；当模型输出
// {"name":..., "arguments":...} 形式的工具调用时，翻译成标准 tool_calls 响应，
// 由调用方执行后把结果以 role=tool 消息带回下一轮。

type openaiMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type openaiToolFn struct {
	Name      string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters any    `json:"parameters,omitempty"`
}

type openaiTool struct {
	Type     string       `json:"type"`
	Function openaiToolFn `json:"function"`
}

type openaiToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function openaiToolCallFn `json:"function"`
}

type openaiChatRequest struct {
	Model    string          `json:"model"`
	Messages []openaiMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	User     string          `json:"user"`
	Tools    []openaiTool    `json:"tools"`
}

type openaiChatText struct {
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content"`
	ToolCalls []openaiToolCall `json:"tool_calls,omitempty"`
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

// voicePrompt 把调用方发来的对话历史展平成单轮提示词，配合一次性会话使用：
// 会话不复用，历史不落地，打断或并发时互不串台；同时约束语音回答的形态。
// tools 非空时注入工具调用协议，实现 OpenAI function calling 桥接。
func voicePrompt(messages []openaiMessage, tools []openaiTool) string {
	last, _ := lastUserText(messages)
	var b strings.Builder
	b.WriteString("你是小智语音助手，回答务必简短口语化，一般不超过三句话，回答里不要出现代码、命令、链接或路径，用日常口语描述。可以大胆使用工具来完成用户的请求（读文件、查状态、执行任务都可以），调用了工具之后必须再用一两句话把结果口头汇报出来。如果需要执行命令或查资料，先用「好嘞」「我看看」这类简短回应开头，再开始干活。\n")
	if len(messages) > 1 {
		b.WriteString("\n对话历史：\n")
		for _, m := range messages {
			text := m.text()
			if text == "" {
				continue
			}
			switch m.Role {
			case "user":
				b.WriteString("用户：" + text + "\n")
			case "assistant":
				b.WriteString("助手：" + text + "\n")
			case "tool", "function":
				b.WriteString("工具结果：" + text + "\n")
			}
		}
	}
	if len(tools) > 0 {
		b.WriteString("\n除上述对话外，你还可以调用以下工具：\n")
		for _, t := range tools {
			b.WriteString(fmt.Sprintf("- %s：%s\n", t.Function.Name, t.Function.Description))
		}
		b.WriteString("需要调用工具时，不要用自然语言回答，只输出一行 JSON：{\"name\":\"工具名\",\"arguments\":{参数}}\n不需要工具时正常口语回答。\n")
	}
	b.WriteString("\n当前用户消息：" + last)
	return b.String()
}

// parseToolCall 从模型输出里提取 {"name":..., "arguments":...} 形式的工具调用，
// 且工具名必须出现在调用方提供的 tools 里才算数。
func parseToolCall(text string, tools []openaiTool) (string, string, bool) {
	trimmed := strings.TrimSpace(text)
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return "", "", false
	}
	end := strings.LastIndex(trimmed, "}")
	if end <= start {
		return "", "", false
	}
	var parsed struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal([]byte(trimmed[start:end+1]), &parsed) != nil || parsed.Name == "" {
		return "", "", false
	}
	for _, t := range tools {
		if t.Function.Name == parsed.Name {
			args := string(parsed.Arguments)
			if args == "" {
				args = "{}"
			}
			return parsed.Name, args, true
		}
	}
	return "", "", false
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
	if _, ok := lastUserText(req.Messages); !ok {
		writeError(w, 400, "messages 中没有用户消息")
		return
	}
	// 语音等一次性调用方：每个请求用全新会话，历史由调用方在 messages 里带过来
	// （见 voicePrompt），避免跨轮累积导致上下文污染与打断时的串台。
	sessionID := "openai-anon"
	if req.User != "" {
		sessionID = "openai-" + req.User
	}
	sessionID = fmt.Sprintf("%s-%d", sessionID, time.Now().UnixNano())
	run, _, err := s.startRun(sessionID, voicePrompt(req.Messages, req.Tools), "")
	if err != nil {
		runHTTPError(w, err)
		return
	}
	if req.Stream {
		s.openaiChatStream(w, r, run, req.Model, req.Tools)
		return
	}
	assistant, err := s.waitRun(r.Context(), run)
	if err != nil {
		runHTTPError(w, err)
		return
	}
	text := strings.TrimSpace(assistant.Text)
	if name, args, ok := parseToolCall(text, req.Tools); ok {
		stop := "tool_calls"
		writeOpenAIJSON(w, openaiChatResponse{
			ID: "chatcmpl-" + run.ID, Object: "chat.completion", Created: time.Now().Unix(), Model: req.Model,
			Choices: []openaiChatChoice{{Index: 0, Message: &openaiChatText{Role: "assistant", ToolCalls: []openaiToolCall{{
				ID: "call_" + run.ID, Type: "function",
				Function: openaiToolCallFn{Name: name, Arguments: args},
			}}}, FinishReason: &stop}},
		})
		return
	}
	if text == "" {
		text = "这个我得动手查一下，先不告诉你结果哈"
	}
	stop := "stop"
	writeOpenAIJSON(w, openaiChatResponse{
		ID: "chatcmpl-" + run.ID, Object: "chat.completion", Created: time.Now().Unix(), Model: req.Model,
		Choices: []openaiChatChoice{{Index: 0, Message: &openaiChatText{Role: "assistant", Content: text}, FinishReason: &stop}},
	})
}

// openaiChatStream 把会话运行的 text_delta 事件翻译成 OpenAI chunk 流；
// 重放/重复请求的处理与 chatStream 保持一致。
func (s *Server) openaiChatStream(w http.ResponseWriter, r *http.Request, run *sessionRun, model string, tools []openaiTool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	var full strings.Builder
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
	chunk := func(delta *openaiChatText, finish string) {
		reason := (*string)(nil)
		if finish != "" {
			reason = &finish
		}
		flush(openaiChatResponse{
			ID: "chatcmpl-" + run.ID, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model,
			Choices: []openaiChatChoice{{Index: 0, Delta: delta, FinishReason: reason}},
		}, false)
	}

	observer := &wsConn{outgoing: make(chan []byte, 512), done: make(chan struct{})}
	defer observer.close()
	defer s.unsubscribeRun(observer, run.SessionID)
	emitted := false
	fallbackIfSilent := func() {
		if !emitted {
			chunk(&openaiChatText{Content: "这个我得动手查一下，先不告诉你结果哈"}, "")
		}
	}
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
		text := strings.TrimSpace(assistant.Text)
		if name, args, ok := parseToolCall(text, tools); ok {
			chunk(&openaiChatText{ToolCalls: []openaiToolCall{{
				ID: "call_" + run.ID, Type: "function",
				Function: openaiToolCallFn{Name: name, Arguments: args},
			}}}, "tool_calls")
			return
		}
		if text == "" {
			text = "这个我得动手查一下，先不告诉你结果哈"
		}
		chunk(&openaiChatText{Role: "assistant", Content: text}, "stop")
		return
	}
	emit := func(event agent.AgentStreamEvent) {
		if event.Type == agent.StreamEventTextDelta && event.TextDelta != "" {
			emitted = true
			full.WriteString(event.TextDelta)
			chunk(&openaiChatText{Content: event.TextDelta}, "")
		}
	}
	if events, ok := snapshot.Events.([]agent.AgentStreamEvent); ok {
		for _, event := range events {
			emit(event)
		}
	}
	if snapshot.Run != nil && !runActive(snapshot.Run) {
		if name, args, ok := parseToolCall(full.String(), tools); ok {
			chunk(&openaiChatText{ToolCalls: []openaiToolCall{{
				ID: "call_" + run.ID, Type: "function",
				Function: openaiToolCallFn{Name: name, Arguments: args},
			}}}, "tool_calls")
			return
		}
		fallbackIfSilent()
		chunk(nil, "stop")
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
					if name, args, ok := parseToolCall(full.String(), tools); ok {
						chunk(&openaiChatText{ToolCalls: []openaiToolCall{{
							ID: "call_" + run.ID, Type: "function",
							Function: openaiToolCallFn{Name: name, Arguments: args},
						}}}, "tool_calls")
						return
					}
					fallbackIfSilent()
					chunk(nil, "stop")
					return
				}
			}
			if event.Type == "status" && event.State != "running" && event.State != "waiting_confirmation" {
				if name, args, ok := parseToolCall(full.String(), tools); ok {
					chunk(&openaiChatText{ToolCalls: []openaiToolCall{{
						ID: "call_" + run.ID, Type: "function",
						Function: openaiToolCallFn{Name: name, Arguments: args},
					}}}, "tool_calls")
					return
				}
				fallbackIfSilent()
				chunk(nil, "stop")
				return
			}
		}
	}
}

func writeOpenAIJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
