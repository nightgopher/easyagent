package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/nightgopher/easyagent/sdk/tools"
)

type toolApproval struct {
	ID                                  string          `json:"confirmation_id"`
	ToolName                            string          `json:"tool_name"`
	Description                         string          `json:"description"`
	Args                                json.RawMessage `json:"args"`
	sessionID, runID, messageID, cardID string
	created                             time.Time
	resolved                            string
}

// Only the paired owner receives details. Shared progress cards never contain
// tool arguments, command descriptions, or the approval controls.
func (h *Handler) sendToolApproval(ctx context.Context, sessionID, runID string, value toolApproval) error {
	if h.access == nil || h.client == nil || value.ID == "" || sessionID == "" || runID == "" {
		return fmt.Errorf("approval delivery is unavailable")
	}
	h.access.mu.Lock()
	owner := h.access.state.Owner
	h.access.mu.Unlock()
	if owner == "" {
		return fmt.Errorf("approval owner is not paired")
	}
	h.approvalsMu.Lock()
	if h.approvals == nil {
		h.approvals = map[string]*toolApproval{}
	}
	for id, pending := range h.approvals {
		if time.Since(pending.created) > 31*time.Minute {
			delete(h.approvals, id)
		}
	}
	if h.approvals[value.ID] != nil {
		h.approvalsMu.Unlock()
		return nil
	}
	value.sessionID, value.runID, value.created = sessionID, runID, time.Now()
	card := approvalCard(value, "")
	h.approvals[value.ID] = &value
	h.approvalsMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cardID, err := h.client.createCardKitCard(ctx, card)
	var messageID string
	if err == nil {
		messageID, err = h.client.sendCardKitMessage(ctx, owner, cardID, "")
		if err == nil && messageID == "" {
			err = fmt.Errorf("approval message has no identity")
		}
	}
	h.approvalsMu.Lock()
	if err != nil {
		delete(h.approvals, value.ID)
		h.approvalsMu.Unlock()
		return err
	}
	value.cardID, value.messageID = cardID, messageID
	finished := value
	h.approvalsMu.Unlock()
	if finished.resolved != "" {
		return h.client.updateCardKitCard(ctx, cardID, approvalCard(finished, finished.resolved), 2)
	}
	return nil
}

func approvalText(text string) string {
	text = html.EscapeString(text)
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]").Replace(text)
}

func approvalCard(p toolApproval, status string) map[string]any {
	detail := p.Description
	if len([]rune(detail)) > 6000 {
		detail = string([]rune(detail)[:6000]) + "\n（详情过长，请在同一主机会话中查看完整内容）"
	}
	elements := []any{map[string]any{"tag": "markdown", "content": "**工具审批 · " + approvalText(p.ToolName) + "**\n\n" + approvalText(detail)}}
	if status != "" {
		elements = append(elements, map[string]any{"tag": "markdown", "content": approvalText(status)})
	} else {
		approveLabel := "允许一次"
		approve := approvalButton(approveLabel, "primary", p.ID, "allow")
		if p.ToolName == "ask_user_question" {
			questions, err := tools.ParseAskUserQuestions(p.Args)
			if err == nil {
				var fields []any
				for i, q := range questions {
					var text strings.Builder
					text.WriteString(q.Question)
					for _, option := range q.Options {
						fmt.Fprintf(&text, "\n• %s：%s", option.Label, option.Description)
					}
					fields = append(fields, map[string]any{"tag": "markdown", "content": approvalText(text.String())}, map[string]any{"tag": "input", "name": fmt.Sprintf("answer_%d", i), "placeholder": plainText("填写选项或自己的回答"), "required": true})
				}
				approve["text"], approve["name"], approve["form_action_type"] = plainText("提交回答"), "submit_answers", "submit"
				fields = append(fields, approve)
				elements = append(elements, map[string]any{"tag": "form", "name": "questions", "elements": fields})
			}
		} else if len([]rune(p.Description)) <= 6000 {
			elements = append(elements, approve)
		}
		elements = append(elements, approvalButton("拒绝 / 跳过", "danger", p.ID, "deny"), map[string]any{"tag": "markdown", "content": "仅本次操作有效。未批准、超时或取消都不会执行。"})
	}
	return map[string]any{"schema": "2.0", "config": map[string]any{"update_multi": true}, "body": map[string]any{"elements": elements}}
}

func approvalButton(label, style, id, decision string) map[string]any {
	return map[string]any{"tag": "button", "text": plainText(label), "type": style,
		"behaviors": []any{map[string]any{"type": "callback", "value": map[string]any{"approval_id": id, "decision": decision}}}}
}

func (h *Handler) handleToolApproval(ctx context.Context, event *callback.CardActionTriggerEvent) *callback.CardActionTriggerResponse {
	id := stringValue(event.Event.Action.Value["approval_id"])
	choice := stringValue(event.Event.Action.Value["decision"])
	if choice != "allow" && choice != "deny" {
		return cardToast("error", "无效的审批操作")
	}
	h.approvalsMu.Lock()
	pending := h.approvals[id]
	var p toolApproval
	if pending != nil {
		p = *pending
	}
	h.approvalsMu.Unlock()
	if pending == nil || event.Event.Context == nil || p.messageID == "" || event.Event.Context.OpenMessageID != p.messageID {
		return cardToast("warning", "审批已处理或已过期，请查看最新卡片")
	}
	approved := choice == "allow"
	body := map[string]any{"run_id": p.runID, "confirmation_id": p.ID, "approved": approved, "reason": "飞书使用者拒绝了本次操作"}
	if approved {
		body["reason"] = "飞书使用者批准本次操作"
	}
	if approved && p.ToolName == "ask_user_question" {
		questions, err := tools.ParseAskUserQuestions(p.Args)
		if err != nil {
			return cardToast("error", "问题格式无效")
		}
		answers := make([]tools.AskUserAnswer, len(questions))
		for i := range questions {
			answers[i].Text = strings.TrimSpace(stringValue(event.Event.Action.FormValue[fmt.Sprintf("answer_%d", i)]))
		}
		encoded, _ := json.Marshal(answers)
		if _, err := tools.ValidateAskUserAnswers(p.Args, encoded); err != nil {
			return cardToast("warning", "请填写每个问题的回答")
		}
		body["answers"] = answers
	}
	encoded, _ := json.Marshal(body)
	// Feishu callbacks have a short acknowledgement deadline. Network failures
	// leave the pending request intact; retrying is safe at the core boundary.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", h.piAgentURL+"/sessions/"+url.PathEscape(p.sessionID)+"/run/confirm", bytes.NewReader(encoded))
	if err != nil {
		return cardToast("error", "审批服务不可用，请重试")
	}
	req.Header.Set("Content-Type", "application/json")
	setAgentAuth(req, h.piAgentAPIKey)
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return cardToast("error", "未收到审批确认，请重试")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusConflict {
		return cardToast("warning", "审批已处理或已过期，未重复执行")
	}
	if resp.StatusCode != http.StatusOK {
		return cardToast("error", "审批未成功，请重试")
	}
	if approved {
		return cardToast("success", "已提交本次批准")
	}
	return cardToast("success", "已拒绝，本次操作不会执行")
}

func (h *Handler) finishToolApproval(ctx context.Context, id string, approved bool, reason string) {
	status := "已批准本次操作"
	if !approved {
		status = "未批准，操作未执行"
	}
	if reason != "" {
		status += "\n" + reason
	}
	h.approvalsMu.Lock()
	p := h.approvals[id]
	delete(h.approvals, id)
	var value toolApproval
	if p != nil {
		p.resolved = status
		value = *p
	}
	h.approvalsMu.Unlock()
	if p == nil || value.cardID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = h.client.updateCardKitCard(ctx, value.cardID, approvalCard(value, status), 2)
}
