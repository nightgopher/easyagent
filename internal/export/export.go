// Package export 把一个会话的逻辑历史([]ai.Message)渲染为可交付的
// 导出文件(Markdown / JSON / 纯文本)。渲染语义与 GET /sessions/{id}/messages
// 一致:compaction 已展开、micro-compaction 已还原、thinking 不导出。
package export

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nightgopher/easyagent/sdk/ai"
)

// Format 导出文件格式。
type Format string

const (
	FormatMarkdown Format = "md"
	FormatJSON     Format = "json"
	FormatText     Format = "txt"
)

// ValidFormat 报告 format 是否为受支持的值。
func ValidFormat(format string) bool {
	switch Format(format) {
	case FormatMarkdown, FormatJSON, FormatText:
		return true
	}
	return false
}

// Options 控制渲染行为。
type Options struct {
	Format       Format
	IncludeTools bool // false 时跳过 assistant.tool_calls 与 tool 结果消息
	SessionID    string
	Title        string
}

// Result 是一次渲染的产物。
type Result struct {
	Content     []byte
	Filename    string
	ContentType string
}

// toolContentLimit 单条工具结果的最大导出字符数(rune),超出截断。
const toolContentLimit = 2000

// Render 把逻辑历史渲染为导出文件。messages 应来自
// session.Session.BuildDisplayContext。
func Render(messages []ai.Message, opts Options) (Result, error) {
	if !ValidFormat(string(opts.Format)) {
		return Result{}, fmt.Errorf("unsupported format %q (supported: md, json, txt)", string(opts.Format))
	}
	var content []byte
	switch opts.Format {
	case FormatJSON:
		var err error
		if content, err = renderJSON(messages, opts); err != nil {
			return Result{}, err
		}
	case FormatText:
		content = []byte(renderText(messages, opts))
	default:
		content = []byte(renderMarkdown(messages, opts))
	}
	return Result{
		Content:     content,
		Filename:    Filename(opts),
		ContentType: contentType(opts.Format),
	}, nil
}

func contentType(format Format) string {
	switch format {
	case FormatJSON:
		return "application/json"
	case FormatText:
		return "text/plain; charset=utf-8"
	default:
		return "text/markdown; charset=utf-8"
	}
}

// Filename 生成导出文件名:<净化标题>-<时间>-<id尾8位>.<ext>。
func Filename(opts Options) string {
	ext := string(opts.Format)
	base := sanitizeFilename(opts.Title)
	if base == "" {
		base = sanitizeFilename(opts.SessionID)
	}
	if base == "" {
		base = "session"
	}
	return fmt.Sprintf("%s-%s-%s.%s", base, time.Now().Format("20060102-1504"), tailID(opts.SessionID), ext)
}

var filenameRe = regexp.MustCompile(`[^\p{L}\p{N}_-]+`)

// sanitizeFilename 过滤路径分隔符与文件系统非法字符,合并连缀分隔符。
func sanitizeFilename(name string) string {
	cleaned := strings.TrimSpace(filenameRe.ReplaceAllString(strings.TrimSpace(name), "-"))
	cleaned = strings.Trim(cleaned, "-.")
	if len(cleaned) > 60 {
		cleaned = strings.TrimRight(cleaned[:60], "-.")
	}
	return cleaned
}

func tailID(id string) string {
	id = strings.TrimPrefix(id, "sess_")
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func userText(message ai.UserMessage) string {
	if message.DisplayText != "" {
		return message.DisplayText
	}
	var b strings.Builder
	for _, block := range message.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		} else if block.Type == "image" {
			b.WriteString("[图片]")
		}
	}
	return b.String()
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + fmt.Sprintf("…(截断,共 %d 字符)", len(runes))
}

func renderMarkdown(messages []ai.Message, opts Options) string {
	var b strings.Builder
	b.WriteString("# 会话导出:" + opts.Title + "\n\n")
	b.WriteString("- 会话 ID:`" + opts.SessionID + "`\n")
	b.WriteString("- 导出时间:" + time.Now().Format("2006-01-02 15:04:05") + "\n")
	b.WriteString(fmt.Sprintf("- 消息数:%d\n", len(messages)))
	b.WriteString(fmt.Sprintf("- 包含工具调用:%s\n", map[bool]string{true: "是", false: "否"}[opts.IncludeTools]))
	b.WriteString("\n---\n\n")

	for _, message := range messages {
		switch msg := message.(type) {
		case ai.UserMessage:
			fmt.Fprintf(&b, "## 👤 用户\n\n%s\n\n", truncate(userText(msg), 20000))
		case ai.AssistantMessage:
			b.WriteString("## 🤖 助手\n\n")
			if text := strings.TrimSpace(msg.Text); text != "" {
				b.WriteString(truncate(text, 20000) + "\n\n")
			}
			if msg.ErrorMsg != "" {
				fmt.Fprintf(&b, "> ⚠️ 出错:%s\n\n", msg.ErrorMsg)
			}
			if opts.IncludeTools {
				for _, call := range msg.ToolCalls {
					fmt.Fprintf(&b, "**🔧 工具调用 `%s`**\n\n```json\n%s\n```\n\n", call.Name, truncate(call.Args, 4000))
				}
			}
		case ai.ToolResultMessage:
			if !opts.IncludeTools {
				continue
			}
			head := "**↩︎ 工具结果**"
			if msg.IsError {
				head = "**⚠️ 工具结果(出错)**"
			}
			fmt.Fprintf(&b, "%s", head)
			if msg.DurationMS > 0 {
				fmt.Fprintf(&b, "(%d ms)", msg.DurationMS)
			}
			b.WriteString("\n\n```\n" + truncate(msg.Content, toolContentLimit) + "\n```\n\n")
		}
	}
	return b.String()
}

// exportedMessage 是 JSON 格式的单条消息投影。
type exportedMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	ToolCalls  []ai.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	IsError    bool          `json:"is_error,omitempty"`
	DurationMS int64         `json:"duration_ms,omitempty"`
}

func renderJSON(messages []ai.Message, opts Options) ([]byte, error) {
	entries := make([]exportedMessage, 0, len(messages))
	for _, message := range messages {
		switch msg := message.(type) {
		case ai.UserMessage:
			entries = append(entries, exportedMessage{Role: string(ai.RoleUser), Content: userText(msg)})
		case ai.AssistantMessage:
			entry := exportedMessage{Role: string(ai.RoleAssistant), Content: msg.Text}
			if opts.IncludeTools {
				entry.ToolCalls = msg.ToolCalls
			}
			if msg.ErrorMsg != "" {
				entry.IsError = true
			}
			entries = append(entries, entry)
		case ai.ToolResultMessage:
			if !opts.IncludeTools {
				continue
			}
			entries = append(entries, exportedMessage{
				Role:       string(ai.RoleTool),
				Content:    msg.Content,
				ToolCallID: msg.ToolCallID,
				IsError:    msg.IsError,
				DurationMS: msg.DurationMS,
			})
		}
	}
	payload := map[string]any{
		"session": map[string]any{
			"id":            opts.SessionID,
			"title":         opts.Title,
			"exported_at":   time.Now().Format(time.RFC3339),
			"message_count": len(entries),
			"include_tools": opts.IncludeTools,
		},
		"messages": entries,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func renderText(messages []ai.Message, opts Options) string {
	var b strings.Builder
	b.WriteString("会话:" + opts.Title + "  ID:" + opts.SessionID + "\n")
	b.WriteString("导出时间:" + time.Now().Format("2006-01-02 15:04:05") + "\n")
	b.WriteString(strings.Repeat("=", 40) + "\n\n")
	for _, message := range messages {
		switch msg := message.(type) {
		case ai.UserMessage:
			fmt.Fprintf(&b, "[用户] %s\n\n", truncate(userText(msg), 20000))
		case ai.AssistantMessage:
			if text := strings.TrimSpace(msg.Text); text != "" {
				fmt.Fprintf(&b, "[助手] %s\n\n", truncate(text, 20000))
			}
			if opts.IncludeTools {
				for _, call := range msg.ToolCalls {
					fmt.Fprintf(&b, "[工具调用] %s %s\n", call.Name, truncate(call.Args, 2000))
				}
			}
			if msg.ErrorMsg != "" {
				fmt.Fprintf(&b, "[错误] %s\n\n", msg.ErrorMsg)
			}
		case ai.ToolResultMessage:
			if !opts.IncludeTools {
				continue
			}
			state := "工具结果"
			if msg.IsError {
				state = "工具结果(出错)"
			}
			fmt.Fprintf(&b, "[%s] %s\n\n", state, truncate(msg.Content, toolContentLimit))
		}
	}
	return b.String()
}
