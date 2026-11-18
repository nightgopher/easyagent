package feishu

// 看门狗转交：外部巡查器（easyagent watch）发现新消息后，不再自己用另一个
// 应用的机器人发纯文本，而是把任务托付给桥接——由扫码绑定的机器人在主人
// 平时对话的会话里发流式卡片。审批卡片、send_message 播报与卡片回调都走
// 桥接现成链路，主人可以直接在同一个对话框里回复、接着聊。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ErrRelayUnavailable 表示桥接无法确定投递对象（会话未绑定飞书对话且主人未配对）。
var ErrRelayUnavailable = errors.New("feishu relay target is unavailable")

// ErrRelayNotAdmitted 表示核心没有受理任务（会话不存在、请求被拒等），
// 没有任务开始运行，也没有发出卡片；调用方可在下一轮重试。
var ErrRelayNotAdmitted = errors.New("relay task was not admitted")

// RelayRequest 是托付给桥接的一次任务：在 SessionID 会话里运行 Prompt，
// 结果以流式卡片呈现在该会话绑定的飞书对话中（找不到绑定时发主人私聊）。
type RelayRequest struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
	// RequestID 是稳定的幂等身份：同一批消息重试不会重复执行任务。
	RequestID string `json:"request_id,omitempty"`
	// Title 是卡片标题栏；Preview 是固定在回复前的 Markdown（如新消息摘要）。
	Title   string `json:"title,omitempty"`
	Preview string `json:"preview,omitempty"`
}

// RelayResult 报告托付的处理结果。Status：accepted（核心已受理，卡片在后台
// 持续更新）、completed（任务结束）、steered（会话忙，已作为插话转交）、
// failed（任务受理后出错，已在卡片里说明）。
type RelayResult struct {
	Status string `json:"status"`
	Text   string `json:"text,omitempty"`
}

// relayChannel 是看门狗任务的核心渠道，沿用其权限模式。
const relayChannel = "larkwatch"

// relayCardTemplate 是看门狗卡片标题栏颜色，与普通对话卡片（无标题栏）区分。
const relayCardTemplate = "turquoise"

// StartRelay 在 runCtx（桥接生命周期）里运行托付任务，等到核心受理（或插话、
// 或未受理）就返回；卡片在后台继续流式更新到任务结束。这样调用方不必为
// 等待审批之类的长任务一直挂着，超时重试也不会重复发卡片。
func (h *Handler) StartRelay(runCtx, waitCtx context.Context, req RelayRequest) (RelayResult, error) {
	admitted := make(chan struct{})
	var once sync.Once
	type outcome struct {
		result RelayResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		ctx := withAcceptedHook(runCtx, func() { once.Do(func() { close(admitted) }) })
		result, err := h.Relay(ctx, req)
		finished <- outcome{result, err}
	}()
	select {
	case <-admitted:
		return RelayResult{Status: "accepted"}, nil
	case o := <-finished:
		return o.result, o.err
	case <-waitCtx.Done():
		return RelayResult{}, waitCtx.Err()
	}
}

// Relay 运行一次托付任务并把结果渲染为卡片；阻塞到任务结束。
func (h *Handler) Relay(ctx context.Context, req RelayRequest) (RelayResult, error) {
	req.SessionID, req.Prompt = strings.TrimSpace(req.SessionID), strings.TrimSpace(req.Prompt)
	if req.SessionID == "" || req.Prompt == "" {
		return RelayResult{}, fmt.Errorf("session_id and prompt are required")
	}
	if h.client == nil {
		return RelayResult{}, ErrRelayUnavailable
	}
	chatKey := h.relayChatKey(req.SessionID)
	if chatKey == "" {
		return RelayResult{}, ErrRelayUnavailable
	}
	preview := strings.TrimSpace(req.Preview)
	var header *CardHeader
	if title := strings.TrimSpace(req.Title); title != "" {
		header = &CardHeader{Title: title, Template: relayCardTemplate}
	}

	startTime := time.Now()
	card := h.client.newDeferredStreamingCard(chatKey, preview, header)
	fullText, err := h.streamChatOn(ctx, relayChannel, chatKey, "", req.SessionID, req.Prompt, card, req.RequestID)
	if errors.Is(err, errAgentBusy) {
		if h.steer(ctx, req.SessionID, req.Prompt) {
			h.finishRelayCard(ctx, card, chatKey, steerAckText, &FooterMetrics{Status: "已插话", ElapsedMs: time.Since(startTime).Milliseconds()})
			return RelayResult{Status: "steered"}, nil
		}
		// 插话失败（任务恰好结束的竞态）：按普通任务重投一次。
		fullText, err = h.streamChatOn(ctx, relayChannel, chatKey, "", req.SessionID, req.Prompt, card, req.RequestID)
	}
	var notAdmitted notAdmittedError
	if errors.As(err, &notAdmitted) || errors.Is(err, errAgentBusy) {
		// 没有任务在跑，也还没有发出卡片：交给调用方下一轮重试，不打扰主人。
		return RelayResult{}, fmt.Errorf("%w: %v", ErrRelayNotAdmitted, err)
	}
	metrics := &FooterMetrics{Status: "已完成", ElapsedMs: time.Since(startTime).Milliseconds()}
	status := "completed"
	if err != nil {
		slog.Error("relay stream failed", "sessionID", req.SessionID, "error", err)
		metrics.Status = "Error"
		status = "failed"
		fullText = strings.TrimSpace(fullText + "\n\n❌ 处理出错：`" + errorDetail(err) + "`")
	}
	h.finishRelayCard(ctx, card, chatKey, fullText, metrics)
	if err == nil && fullText != "" {
		h.sendDetectedFiles(ctx, chatKey, fullText, h.workspaceFor(chatKey))
	}
	return RelayResult{Status: status, Text: fullText}, nil
}

// finishRelayCard 写入卡片终态；卡片发不出去时退回 Markdown 消息，保证主人收得到。
func (h *Handler) finishRelayCard(ctx context.Context, card *StreamingCardHandle, chatKey, text string, metrics *FooterMetrics) {
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err := card.Finalize(finalCtx, text, metrics)
	if err == nil {
		return
	}
	slog.Warn("relay card finalize failed; falling back to markdown", "error", err)
	body := card.decorate(text)
	if card.header != nil && card.header.Title != "" {
		body = "**" + card.header.Title + "**\n\n" + body
	}
	if _, sendErr := h.client.SendMarkdown(finalCtx, chatKey, body, ""); sendErr != nil {
		slog.Warn("relay markdown fallback failed", "error", sendErr)
	}
}

// relayChatKey 找到会话绑定的飞书对话；没有绑定时退回主人私聊。
func (h *Handler) relayChatKey(sessionID string) string {
	h.routesMu.Lock()
	var fallback string
	for chatKey, route := range h.routes {
		if route == nil || route.SessionID != sessionID {
			continue
		}
		// 多个对话绑定同一会话时优先私聊（open_id），避免把私人消息摘要发进群。
		if strings.HasPrefix(chatKey, "ou_") {
			h.routesMu.Unlock()
			return chatKey
		}
		if fallback == "" {
			fallback = chatKey
		}
	}
	h.routesMu.Unlock()
	owner := h.ownerOpenID()
	if owner != "" {
		return owner // 群里不发主人私聊的摘要：有主人时宁可发私聊
	}
	return fallback
}

func (h *Handler) ownerOpenID() string {
	if h.access == nil {
		return ""
	}
	h.access.mu.Lock()
	defer h.access.mu.Unlock()
	return h.access.state.Owner
}

type acceptedHookKey struct{}

// withAcceptedHook 让 streamChatOn 在核心受理任务（SSE accepted 事件）时回调 fn。
func withAcceptedHook(ctx context.Context, fn func()) context.Context {
	return context.WithValue(ctx, acceptedHookKey{}, fn)
}

func notifyAccepted(ctx context.Context) {
	if fn, ok := ctx.Value(acceptedHookKey{}).(func()); ok && fn != nil {
		fn()
	}
}
