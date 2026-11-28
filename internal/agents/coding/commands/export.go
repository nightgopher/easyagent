package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgopher/easyagent/internal/export"
	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/session"
	"github.com/nightgopher/easyagent/sdk/slashcmd"
)

const exportUsage = `Usage: /export [--format md|json|txt] [--no-tools] [相对路径]
  --format md|json|txt   导出格式(默认 md)
  --no-tools             不包含工具调用与工具结果
  [相对路径]             相对当前工作区的保存路径(默认 exports/<自动命名>)`

// RegisterExportCommands 注册 /export:把当前会话的完整对话日志导出为
// Markdown / JSON / 纯文本文件,写入会话工作区。
func RegisterExportCommands(registry *slashcmd.Registry) {
	registry.Register(slashcmd.Command{
		Name:        "export",
		Description: "Export the full conversation log to the workspace (/export [--format md|json|txt] [--no-tools] [path])",
		Handler:     handleExport,
	})
}

func handleExport(ctx slashcmd.Context, args string) (slashcmd.CommandResult, error) {
	if ctx.Session == nil {
		return slashcmd.CommandResult{Output: "no active session"}, nil
	}
	format, includeTools, path := parseExportArgs(args)
	if !export.ValidFormat(format) {
		return slashcmd.CommandResult{Output: exportUsage}, nil
	}

	storage, ok := ctx.Session.(interface {
		Session() *session.Session
	})
	if !ok {
		return slashcmd.CommandResult{Output: "当前环境不支持导出(无法访问会话存储)"}, nil
	}
	messages, err := storage.Session().BuildDisplayContext(ctx.Ctx)
	if err != nil {
		return slashcmd.CommandResult{}, err
	}

	opts := export.Options{
		Format:       export.Format(format),
		IncludeTools: includeTools,
		SessionID:    ctx.Session.SessionID(),
		Title:        sessionExportTitle(ctx.Session.SessionID(), messages),
	}
	result, err := export.Render(messages, opts)
	if err != nil {
		return slashcmd.CommandResult{Output: exportUsage}, nil
	}

	if path == "" {
		path = filepath.Join("exports", result.Filename)
	}
	if !filepath.IsLocal(path) {
		return slashcmd.CommandResult{Output: "路径必须是相对当前工作区的合法路径"}, nil
	}
	if filepath.Ext(path) == "" {
		path += "." + format
	}

	workspace, ok := ctx.Session.(interface{ Workspace() string })
	if !ok {
		return slashcmd.CommandResult{Output: "当前环境不支持写文件(无法确定工作区)"}, nil
	}
	root, err := os.OpenRoot(workspace.Workspace())
	if err != nil {
		return slashcmd.CommandResult{Output: "工作区不可用:" + err.Error()}, nil
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return slashcmd.CommandResult{Output: "无法创建导出目录:" + err.Error()}, nil
	}
	f, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return slashcmd.CommandResult{Output: "无法写文件:" + err.Error()}, nil
	}
	if _, err := f.Write(result.Content); err != nil {
		f.Close()
		return slashcmd.CommandResult{Output: "写文件失败:" + err.Error()}, nil
	}
	if err := f.Close(); err != nil {
		return slashcmd.CommandResult{Output: "写文件失败:" + err.Error()}, nil
	}

	abs := filepath.Join(workspace.Workspace(), path)
	KB := float64(len(result.Content)) / 1024
	return slashcmd.CommandResult{Output: fmt.Sprintf("✅ 已导出 %d 条消息(%.1f KB)→ %s", len(messages), KB, abs)}, nil
}

// parseExportArgs 解析 /export 参数:--format X|--format=X、--no-tools,
// 剩余第一个词作为保存路径。解析仿 /loop 的手撕惯例。
func parseExportArgs(args string) (format string, includeTools bool, path string) {
	format = string(export.FormatMarkdown)
	includeTools = true
	fields := strings.Fields(args)
	var rest []string
	for i := 0; i < len(fields); i++ {
		switch {
		case fields[i] == "--no-tools":
			includeTools = false
		case fields[i] == "--format" && i+1 < len(fields):
			i++
			format = strings.ToLower(strings.TrimSpace(fields[i]))
		case strings.HasPrefix(fields[i], "--format="):
			format = strings.ToLower(strings.TrimPrefix(fields[i], "--format="))
		case strings.HasPrefix(fields[i], "--"):
			// 未知 flag 忽略,保持宽松
		default:
			rest = append(rest, fields[i])
		}
	}
	if len(rest) > 0 {
		path = rest[0]
	}
	return format, includeTools, path
}

// sessionExportTitle 取会话标题:会话 ID 之外,优先用首条用户消息
// 开头一段作为可读标题。
func sessionExportTitle(sessionID string, messages []ai.Message) string {
	for _, message := range messages {
		if user, ok := message.(ai.UserMessage); ok {
			text := strings.TrimSpace(user.DisplayText)
			if text == "" {
				for _, block := range user.Content {
					if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
						text = strings.TrimSpace(block.Text)
						break
					}
				}
			}
			if idx := strings.IndexAny(text, "\n。.!?,;:"); idx > 0 {
				text = text[:idx]
			}
			runes := []rune(text)
			if len(runes) > 30 {
				text = string(runes[:30])
			}
			if text != "" {
				return text
			}
			break
		}
	}
	return sessionID
}
