package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgopher/easyagent/sdk/agent"
)

// maxSendFileBytes 与下游通道对齐：飞书文件消息上限 30MB，serve 的
// /workspace/download 端点上限 32MB；工具侧取更保守的 25MB。
const maxSendFileBytes = 25 << 20

// SendFileTool delivers a file generated during the task to the user: chat
// surfaces render it as a file card and remote channels (feishu / mobile)
// deliver the bytes through the workspace download API or a file message.
// The file must already exist inside the workspace; this tool only announces
// and transfers it, it does not create content.
type SendFileTool struct {
	pathPolicy PathPolicy
	workspace  string // 工作目录，用于解析相对路径
}

type SendFileParams struct {
	Path    string `json:"path"`
	Caption string `json:"caption,omitempty"`
}

type SendFileToolOption func(*SendFileTool)

// WithSendFilePathPolicy explicitly controls access outside the workspace.
func WithSendFilePathPolicy(policy PathPolicy) SendFileToolOption {
	return func(t *SendFileTool) { t.pathPolicy = policy }
}

// WithSendFileWorkspace sets the workspace for path resolution.
func WithSendFileWorkspace(ws string) SendFileToolOption {
	return func(t *SendFileTool) { t.workspace = ws }
}

func NewSendFileTool(options ...SendFileToolOption) *SendFileTool {
	t := &SendFileTool{}
	for _, opt := range options {
		opt(t)
	}
	return t
}

func (t *SendFileTool) Name() string { return "send_file" }

func (t *SendFileTool) Description() string {
	return "Deliver a file that you generated during this task to the user: chat windows show it as a downloadable " +
		"file card, and remote channels (feishu / mobile) receive it as a file message. The file must already exist " +
		"inside the workspace — create it with write/patch first, then call send_file with its path. Use it whenever " +
		"the user asked for a document, report, export or any artifact they need to keep, instead of only printing " +
		"the path in text. Not for directories, and not for files outside the workspace."
}

func (t *SendFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Workspace-relative (or absolute) path of the file to deliver.",
			},
			"caption": map[string]any{
				"type":        "string",
				"description": "Optional one-line note shown alongside the file (what it is, why it matters).",
			},
		},
		"required": []string{"path"},
	}
}

func (t *SendFileTool) Validate(raw json.RawMessage) (json.RawMessage, error) {
	var params SendFileParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.Path) == "" {
		return nil, fmt.Errorf("path 不能为空")
	}
	if len(params.Caption) > 500 {
		return nil, fmt.Errorf("caption 过长（上限 500 字符）")
	}
	return raw, nil
}

// IsConcurrencySafe marks delivery as read-only: parallel batches may include it.
func (t *SendFileTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *SendFileTool) Execute(ctx context.Context, raw json.RawMessage, _ func(agent.PartialResult)) (agent.ToolResult, error) {
	var params SendFileParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return agent.ToolResult{}, err
	}
	resolved := ResolvePath(t.workspace, params.Path)
	if !t.pathPolicy.Allows(t.workspace, resolved) {
		return agent.ToolResult{IsError: true, Content: "send_file 只能交付工作区内的文件：" + params.Path}, nil
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return agent.ToolResult{IsError: true, Content: "文件不存在：" + params.Path}, nil
	}
	if !info.Mode().IsRegular() {
		return agent.ToolResult{IsError: true, Content: "只能交付普通文件（不是目录）：" + params.Path}, nil
	}
	if info.Size() > maxSendFileBytes {
		return agent.ToolResult{IsError: true, Content: fmt.Sprintf("文件超过交付上限（%d MB）：%s", maxSendFileBytes>>20, params.Path)}, nil
	}

	rel := resolved
	if t.workspace != "" {
		if r, err := filepath.Rel(t.workspace, resolved); err == nil {
			rel = filepath.ToSlash(r)
		}
	}
	name := filepath.Base(resolved)
	caption := strings.TrimSpace(params.Caption)

	userFacing := fmt.Sprintf("已交付文件：%s（%s）", name, humanByteSize(info.Size()))
	if caption != "" {
		userFacing += " — " + caption
	}
	return agent.ToolResult{
		Content:    fmt.Sprintf("File delivered: %s (%d bytes)", rel, info.Size()),
		UserFacing: userFacing,
		Details: map[string]any{
			"kind":    "file",
			"path":    rel,
			"name":    name,
			"size":    info.Size(),
			"mime":    detectSendFileMime(resolved),
			"caption": caption,
		},
	}, nil
}

func detectSendFileMime(path string) string {
	f, err := os.Open(path) // #nosec G304 -- 路径已经过 PathPolicy 校验
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buffer := make([]byte, 512)
	n, _ := f.Read(buffer)
	if mime := http.DetectContentType(buffer[:n]); mime != "" {
		return mime
	}
	return "application/octet-stream"
}

func humanByteSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
