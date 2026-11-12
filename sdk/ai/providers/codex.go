package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nightgopher/easyagent/sdk/ai"
)

// CodexTokenSource supplies the ChatGPT OAuth credential. Same contract as
// DeepVTokenSource: the provider does not own storage.
type CodexTokenSource interface {
	AccessToken(context.Context) (string, error)
	RefreshAfterRejection(context.Context, string) (string, error)
}

// DefaultCodexBaseURL is the ChatGPT backend that serves Codex subscription
// quota. Requests go to <base>/codex/responses.
const DefaultCodexBaseURL = "https://chatgpt.com/backend-api"

// CodexProvider talks to the OpenAI Responses API on the ChatGPT backend,
// authenticating with the user's ChatGPT account (Codex CLI OAuth).
type CodexProvider struct {
	thinking  ThinkingOptions
	accountID string
	tokens    CodexTokenSource
	baseURL   string
	client    *http.Client
}

// NewCodexProvider builds a provider that bills the given ChatGPT account.
func NewCodexProvider(accountID string, tokens CodexTokenSource, options ...ThinkingOptions) *CodexProvider {
	var thinking ThinkingOptions
	if len(options) > 0 {
		thinking = options[0]
	}
	return &CodexProvider{
		thinking:  thinking,
		accountID: accountID,
		tokens:    tokens,
		baseURL:   DefaultCodexBaseURL,
		client:    newStreamHTTPClient(),
	}
}

func (p *CodexProvider) Name() string { return "codex" }

func (p *CodexProvider) StreamSimple(ctx context.Context, req ai.SimpleStreamRequest) (*ai.EventStream, error) {
	return p.Stream(ctx, ai.StreamRequest{
		Model: req.Model, Messages: req.Messages, System: req.System,
		Tools: req.Tools, MaxTokens: req.MaxTokens, Temperature: req.Temperature,
	})
}

// ─── Responses API 线格式 ─────────────────────────────────────────────────────────

func base64Encode(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// buildCodexRequest converts the unified messages into Responses wire format.
func buildCodexRequest(req ai.StreamRequest, effort string) (map[string]any, error) {
	messages := ai.MessagesForModel(req.Messages, req.Model)
	input := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		switch m := msg.(type) {
		case ai.UserMessage:
			content := make([]map[string]any, 0, len(m.Content))
			for _, block := range m.Content {
				switch block.Type {
				case "text":
					if block.Text != "" {
						content = append(content, map[string]any{"type": "input_text", "text": block.Text})
					}
				case "image":
					if block.Image == nil {
						continue
					}
					dataURL := fmt.Sprintf("data:%s;base64,%s", block.Image.MediaType, base64Encode(block.Image.Data))
					content = append(content, map[string]any{"type": "input_image", "image_url": dataURL, "detail": "auto"})
				}
			}
			if len(content) == 0 {
				continue
			}
			input = append(input, map[string]any{"role": "user", "content": content})
		case ai.AssistantMessage:
			// reasoning 回放：Responses API 要求把上一轮 reasoning item 放回 input
			//（store:false 时 encrypted_content 是唯一的记忆载体）。
			for _, tb := range m.ThinkingBlocks {
				if tb.Signature == "" {
					continue
				}
				var item map[string]any
				if err := json.Unmarshal([]byte(tb.Signature), &item); err == nil && item["type"] == "reasoning" {
					input = append(input, item)
				}
			}
			if m.Text != "" {
				input = append(input, map[string]any{
					"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": m.Text, "annotations": []any{}}},
					"status":  "completed",
				})
			}
			for _, tc := range m.ToolCalls {
				input = append(input, map[string]any{
					"type": "function_call", "call_id": tc.ID,
					"name": tc.Name, "arguments": tc.Args,
				})
			}
		case ai.ToolResultMessage:
			output := m.Content
			// 图片结果转 input_image 数组；纯文本保持字符串
			if len(m.Images) > 0 {
				parts := []map[string]any{}
				if strings.TrimSpace(output) != "" {
					parts = append(parts, map[string]any{"type": "input_text", "text": output})
				}
				for _, img := range m.Images {
					parts = append(parts, map[string]any{
						"type": "input_image", "detail": "auto",
						"image_url": fmt.Sprintf("data:%s;base64,%s", img.MediaType, base64Encode(img.Data)),
					})
				}
				input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": parts})
			} else {
				input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": output})
			}
		}
	}

	tools := make([]map[string]any, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, map[string]any{
			"type": "function", "name": tool.Name,
			"description": tool.Description, "parameters": tool.Parameters,
		})
	}

	body := map[string]any{
		"model":               req.Model.ID,
		"instructions":        req.System,
		"input":               input,
		"tools":               tools,
		"store":               false,
		"stream":              true,
		"parallel_tool_calls": true,
		"include":             []string{"reasoning.encrypted_content"},
		"reasoning":           map[string]any{"effort": effort, "summary": "auto"},
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	return body, nil
}

func resolveCodexEffort(options ThinkingOptions) string {
	if options.Effort != "" {
		return options.Effort
	}
	return "medium"
}

// ─── SSE 解析 ────────────────────────────────────────────────────────────────────

type codexSSEEvent struct {
	Type  string          `json:"type"`
	Item  codexOutputItem `json:"item"`
	Delta string          `json:"delta"`
	Text  string          `json:"text"`
	// response.completed / response.incomplete 载荷
	Response *codexResponsePayload `json:"response"`
	// error 事件
	Code    string `json:"code"`
	Message string `json:"message"`
}

type codexOutputItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Arguments string `json:"arguments"`
	// reasoning item 的原始 JSON，用于回放
	Raw json.RawMessage `json:"-"`
}

type codexResponsePayload struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	IncompleteReason *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		TotalTokens        int `json:"total_tokens"`
		InputTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

func (p *CodexProvider) handleSSE(ctx context.Context, stream *ai.EventStream, body io.Reader, partial *ai.StreamAssistantMessage, wd *streamWatchdog) (received bool, err error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)

	var eventName string
	// 每个 output_index 的累积状态
	type slot struct {
		kind      string // "text" | "tool" | "reasoning"
		callID    string
		name      string
		arguments strings.Builder
		reasoning strings.Builder
		signature string
	}
	slots := map[int]*slot{}
	var currentText strings.Builder
	textStarted := false
	toolOrder := []int{}
	terminalSeen := false

	pushTextStart := func() {
		if !textStarted {
			textStarted = true
			_ = stream.Push(ctx, ai.EventTextStart{ContentIndex: 0, Partial: *partial})
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		wd.Kick()
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(line[len("event:"):])
			continue
		case !strings.HasPrefix(line, "data:"):
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if eventName == "" {
			// 无 event 行时从 data.type 推断
			var probe struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(payload), &probe) == nil {
				eventName = probe.Type
			}
		}
		var ev codexSSEEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			eventName = ""
			continue
		}

		switch eventName {
		case "response.output_item.added":
			switch ev.Item.Type {
			case "function_call":
				slots[0] = &slot{kind: "tool", callID: ev.Item.CallID, name: ev.Item.Name}
				_ = stream.Push(ctx, ai.EventToolCallStart{ContentIndex: 1, Partial: *partial})
			case "reasoning":
				slots[0] = &slot{kind: "reasoning"}
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if s := slots[0]; s != nil {
				s.reasoning.WriteString(ev.Delta)
				partial.Thinking += ev.Delta
				_ = stream.Push(ctx, ai.EventThinkingDelta{Delta: ev.Delta})
			}
		case "response.output_text.delta":
			pushTextStart()
			currentText.WriteString(ev.Delta)
			_ = stream.Push(ctx, ai.EventTextDelta{ContentIndex: 0, Delta: ev.Delta, Partial: *partial})
		case "response.refusal.delta":
			pushTextStart()
			currentText.WriteString(ev.Delta)
			_ = stream.Push(ctx, ai.EventTextDelta{ContentIndex: 0, Delta: ev.Delta, Partial: *partial})
		case "response.function_call_arguments.delta":
			if s := slots[0]; s != nil && s.kind == "tool" {
				s.arguments.WriteString(ev.Delta)
				_ = stream.Push(ctx, ai.EventToolCallDelta{ContentIndex: 1, Delta: ev.Delta, Partial: *partial})
			}
		case "response.output_item.done":
			if s := slots[0]; s != nil {
				switch s.kind {
				case "reasoning":
					// 用 done item 的完整 JSON 作为回放签名（含 encrypted_content）
					s.signature = payload
				case "tool":
					args := s.arguments.String()
					// done item 的 arguments 更完整时以其为准
					if ev.Item.Arguments != "" {
						args = ev.Item.Arguments
					}
					tc := ai.ToolCall{ID: s.callID, Name: s.name, Args: args}
					partial.ToolCalls = append(partial.ToolCalls, tc)
					_ = stream.Push(ctx, ai.EventToolCallEnd{ContentIndex: 1 + len(toolOrder), ToolCall: tc, Partial: *partial})
					toolOrder = append(toolOrder, 0)
				}
				delete(slots, 0)
			}
		case "response.completed", "response.incomplete":
			terminalSeen = true
			if ev.Response != nil {
				if u := ev.Response.Usage; u != nil {
					var cached *int
					if u.InputTokensDetails != nil && u.InputTokensDetails.CachedTokens > 0 {
						cached = &u.InputTokensDetails.CachedTokens
					}
					partial.Usage = ai.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CachedInputTokens: cached}
				}
				switch ev.Response.Status {
				case "completed":
					partial.StopReason = ai.StopReasonStop
				case "incomplete":
					if ev.Response.IncompleteReason != nil && ev.Response.IncompleteReason.Reason == "max_output_tokens" {
						partial.StopReason = ai.StopReasonLength
					} else {
						partial.StopReason = ai.StopReasonStop
					}
				default:
					partial.StopReason = ai.StopReasonError
					partial.ErrorMsg = fmt.Sprintf("Codex 响应状态 %s", ev.Response.Status)
				}
			}
		case "response.failed":
			terminalSeen = true
			msg := ev.Message
			if msg == "" {
				msg = "Codex 响应失败"
			}
			return received, fmt.Errorf("codex 响应失败: %s", msg)
		case "error":
			msg := ev.Message
			if msg == "" {
				msg = payload
			}
			return received, fmt.Errorf("codex 流错误: %s", msg)
		}
		eventName = ""
	}

	// 关闭文本 block
	if textStarted {
		partial.Text = currentText.String()
		_ = stream.Push(ctx, ai.EventTextEnd{ContentIndex: 0, Text: partial.Text, Partial: *partial})
	}

	received = textStarted || len(toolOrder) > 0 || partial.Thinking != ""

	if err := scanner.Err(); err != nil {
		return received, err
	}
	if !received {
		return false, nil
	}
	if !terminalSeen {
		return received, errors.New("codex 流在终止事件前结束")
	}
	if partial.ToolCalls != nil && partial.StopReason == ai.StopReasonStop {
		partial.StopReason = ai.StopReasonToolUse
	}
	if partial.StopReason == "" {
		partial.StopReason = ai.StopReasonStop
		partial.StopReasonMissing = true
	}
	_ = stream.Push(ctx, ai.EventDone{Reason: partial.StopReason, Message: *partial})
	stream.SetResult(*partial, nil)
	return true, nil
}

// codexReasoningSignature 提取 reasoning 回放签名（完整 item JSON）。
func codexReasoningSignature(itemJSON string) string { return itemJSON }

// ─── Stream ──────────────────────────────────────────────────────────────────────

func (p *CodexProvider) Stream(ctx context.Context, req ai.StreamRequest) (*ai.EventStream, error) {
	if err := p.thinking.Validate(p.Name()); err != nil {
		return nil, err
	}
	idle, err := configuredStreamIdleTimeout()
	if err != nil {
		return nil, err
	}
	maxRetries, err := configuredModelMaxRetries()
	if err != nil {
		return nil, err
	}
	if p.tokens == nil {
		return nil, errors.New("请先登录 Codex（ChatGPT）账号")
	}

	wire, err := buildCodexRequest(req, resolveCodexEffort(p.thinking))
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	stream := ai.NewEventStream(8)
	go func() {
		defer stream.Close()
		partial := ai.StreamAssistantMessage{}
		_ = stream.Push(ctx, ai.EventStart{Partial: partial})

		retriesMade := 0
		fail := func(err error) {
			if retriesMade > 0 && ctx.Err() == nil {
				err = fmt.Errorf("模型请求自动重试 %d 次后仍失败: %w", retriesMade, err)
			}
			partial.StopReason = ai.StopReasonError
			partial.ErrorMsg = err.Error()
			_ = stream.Push(context.Background(), ai.EventError{Reason: "error", Error: err.Error()})
			stream.SetResult(partial, err)
		}

		token, err := p.tokens.AccessToken(ctx)
		if err != nil {
			fail(err)
			return
		}

		httpRetries := 0
		refreshed := false
		for attempt := 1; ; attempt++ {
			streamCtx, cancel := context.WithCancel(ctx)
			httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost,
				strings.TrimRight(p.baseURL, "/")+"/codex/responses", bytes.NewReader(body))
			if err != nil {
				cancel()
				fail(fmt.Errorf("create request: %w", err))
				return
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Authorization", "Bearer "+token)
			httpReq.Header.Set("chatgpt-account-id", p.accountID)
			httpReq.Header.Set("OpenAI-Beta", "responses=experimental")
			httpReq.Header.Set("originator", "codex_cli")
			httpReq.Header.Set("Accept", "text/event-stream")

			resp, err := p.client.Do(httpReq)
			if err != nil {
				cancel()
				if ctx.Err() == nil && retryableTransportError(err) && httpRetries < maxRetries {
					httpRetries++
					slog.Warn("model request transport error, retrying",
						"provider", p.Name(), "attempt", attempt, "retries", httpRetries, "err", err)
					if _, ok := waitModelRetry(ctx, httpRetries, 0, reportModelRetry(ctx, stream, httpRetries, maxRetries, nil)); ok {
						retriesMade++
						continue
					}
				}
				fail(err)
				return
			}
			if resp.StatusCode == http.StatusUnauthorized {
				_ = resp.Body.Close()
				cancel()
				// 一次请求只刷新一次：服务端未换发新 token 时重试也只会再次 401
				if refreshed {
					fail(errors.New("codex 登录已失效，请重新登录"))
					return
				}
				refreshed = true
				newToken, refreshErr := p.tokens.RefreshAfterRejection(ctx, token)
				if refreshErr != nil {
					fail(refreshErr)
					return
				}
				if newToken == "" || newToken == token {
					fail(errors.New("codex 登录已失效，请重新登录"))
					return
				}
				token = newToken
				continue
			}
			if resp.StatusCode != http.StatusOK {
				statusErr := modelHTTPError(resp, p.Name(), token)
				_ = resp.Body.Close()
				cancel()
				if statusErr.Retryable() && ctx.Err() == nil && httpRetries < maxRetries {
					httpRetries++
					slog.Warn("model request failed, retrying",
						"provider", p.Name(), "attempt", attempt, "retries", httpRetries,
						"status", resp.StatusCode, "retry_after", statusErr.RetryAfter)
					if _, ok := waitModelRetry(ctx, httpRetries, statusErr.RetryAfter, reportModelRetry(ctx, stream, httpRetries, maxRetries, statusErr)); ok {
						retriesMade++
						continue
					}
					if ctx.Err() != nil {
						fail(ctx.Err())
						return
					}
				}
				fail(statusErr)
				return
			}

			wd := startStreamWatchdog(streamCtx, idle, cancel)
			received, err := p.handleSSE(streamCtx, stream, watchedReader{resp.Body, wd}, &partial, wd)
			_ = time.Millisecond // keep time import if unused elsewhere
			resp.Body.Close()
			cancel()
			if wd.Tripped() {
				err = wd.idleError(idle, attempt, time.Now(), time.Now())
			}
			if err == nil && received {
				return
			}
			if err == nil {
				err = errors.New("codex 返回了空流")
			}
			fail(err)
			return
		}
	}()

	return stream, nil
}
