package larkwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Runner 抽象 lark-cli 调用，测试用假实现替换。
type Runner interface {
	// Output 执行一条 lark-cli 命令并返回 stdout。
	Output(ctx context.Context, args ...string) ([]byte, error)
}

// ExecRunner 用真实 lark-cli 二进制执行命令。
type ExecRunner struct {
	// Bin 是 lark-cli 可执行文件路径，默认 "lark-cli"。
	Bin string
}

// lark-cli 的更新/技能提示会污染 stdout 的 JSON，全部关掉。
var larkEnv = append(os.Environ(),
	"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
	"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
)

// Output 实现 Runner。
func (e ExecRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
	bin := e.Bin
	if bin == "" {
		bin = "lark-cli"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = larkEnv
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("lark-cli %s: %w", strings.Join(args, " "), err)
		}
		return nil, fmt.Errorf("lark-cli %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

// envelope 是 lark-cli JSON 输出的统一外壳。
type envelope struct {
	OK       bool            `json:"ok"`
	Identity string          `json:"identity"`
	Data     json.RawMessage `json:"data"`
	Error    *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func decodeEnvelope(out []byte) (json.RawMessage, error) {
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("lark-cli output is not JSON: %w", err)
	}
	if !env.OK {
		msg := "unknown error"
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return nil, fmt.Errorf("lark-cli failed: %s", msg)
	}
	return env.Data, nil
}

// P2PChat 是一条主人参与的单聊会话。
type P2PChat struct {
	ChatID    string
	Name      string
	TargetID  string
	IsBotChat bool // 对端是机器人（含管家自己依赖的机器人会话，恒不巡查）
}

// Message 是一条聊天消息的只读视图。
type Message struct {
	ID       string
	Position int64 // 会话内单调递增
	Time     string
	SenderID string
	Sender   string
	IsText   bool
	Content  string
	Deleted  bool
}

// Lark 封装看门狗需要的 lark-cli 只读/发送能力。
type Lark struct {
	runner Runner
}

// NewLark 构造 Lark。
func NewLark(r Runner) *Lark { return &Lark{runner: r} }

func (l *Lark) run(ctx context.Context, args ...string) (json.RawMessage, error) {
	out, err := l.runner.Output(ctx, args...)
	if err != nil {
		return nil, err
	}
	return decodeEnvelope(out)
}

// SelfOpenID 返回当前登录用户的 open_id（contact +get-user）。
func (l *Lark) SelfOpenID(ctx context.Context) (string, error) {
	data, err := l.run(ctx, "contact", "+get-user", "--as", "user")
	if err != nil {
		return "", err
	}
	var payload struct {
		User struct {
			OpenID string `json:"open_id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("parse get-user: %w", err)
	}
	if payload.User.OpenID == "" {
		return "", fmt.Errorf("get-user returned no open_id")
	}
	return payload.User.OpenID, nil
}

// BotOpenID 返回 lark-cli 配置应用的机器人 open_id（auth status）；取不到时返回空。
func (l *Lark) BotOpenID(ctx context.Context) string {
	out, err := l.runner.Output(ctx, "auth", "status", "--json", "--verify")
	if err != nil {
		return ""
	}
	var payload struct {
		Identities struct {
			Bot struct {
				OpenID string `json:"openId"`
			} `json:"bot"`
		} `json:"identities"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return ""
	}
	return payload.Identities.Bot.OpenID
}

// P2PChats 列出主人参与的全部单聊（自动翻页）。
func (l *Lark) P2PChats(ctx context.Context) ([]P2PChat, error) {
	data, err := l.run(ctx, "im", "+chat-list", "--types=p2p", "--as", "user", "--page-all")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Chats []struct {
			ChatID     string `json:"chat_id"`
			Name       string `json:"name"`
			TargetID   string `json:"p2p_target_id"`
			TargetType string `json:"p2p_target_type"`
		} `json:"chats"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse chat-list: %w", err)
	}
	chats := make([]P2PChat, 0, len(payload.Chats))
	for _, c := range payload.Chats {
		if c.ChatID == "" {
			continue
		}
		chats = append(chats, P2PChat{
			ChatID:    c.ChatID,
			Name:      c.Name,
			TargetID:  c.TargetID,
			IsBotChat: c.TargetType == "bot",
		})
	}
	return chats, nil
}

// LatestMessages 按新→旧返回会话里最新的 limit 条消息。
func (l *Lark) LatestMessages(ctx context.Context, chatID string, limit int) ([]Message, error) {
	data, err := l.run(ctx, "im", "+chat-messages-list",
		"--chat-id", chatID, "--limit", fmt.Sprint(limit), "--order", "desc", "--as", "user")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Messages []struct {
			MessageID  string `json:"message_id"`
			Position   string `json:"message_position"`
			CreateTime string `json:"create_time"`
			MsgType    string `json:"msg_type"`
			Content    string `json:"content"`
			Deleted    bool   `json:"deleted"`
			Sender     struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"sender"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse chat-messages-list: %w", err)
	}
	msgs := make([]Message, 0, len(payload.Messages))
	for _, m := range payload.Messages {
		pos, _ := strconv.ParseInt(m.Position, 10, 64)
		msgs = append(msgs, Message{
			ID:       m.MessageID,
			Position: pos,
			Time:     m.CreateTime,
			SenderID: m.Sender.ID,
			Sender:   m.Sender.Name,
			IsText:   m.MsgType == "text",
			Content:  strings.TrimSpace(m.Content),
			Deleted:  m.Deleted,
		})
	}
	return msgs, nil
}

// SendToChat 以指定身份向会话发送文本（identity: "bot" 或 "user"）。
func (l *Lark) SendToChat(ctx context.Context, chatID, text, identity string) error {
	_, err := l.run(ctx, "im", "+messages-send", "--chat-id", chatID,
		"--text", text, "--as", identity)
	return err
}
