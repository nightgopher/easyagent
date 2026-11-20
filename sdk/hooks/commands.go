package hooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nightgopher/easyagent/sdk/agent"
)

// CommandHook is explicitly configured on the host; project files are never
// discovered or executed as hooks.
type CommandHook struct {
	Event          string   `json:"event"`
	Tool           string   `json:"tool"`
	Command        string   `json:"command"`
	Args           []string `json:"args,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	Enabled        bool     `json:"enabled"`
}

type CommandSettings struct {
	Hooks []CommandHook `json:"hooks"`
}

var commandSettingsMu sync.Mutex
var ErrCommandSettingsChanged = errors.New("hooks 已在其他位置更改，请刷新后重试")

func CommandSettingsRevision(settings CommandSettings) string {
	if settings.Hooks == nil {
		settings.Hooks = []CommandHook{}
	}
	data, _ := json.Marshal(settings)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func ValidateCommandSettings(settings CommandSettings) error {
	if len(settings.Hooks) > 32 {
		return errors.New("最多配置 32 个 hooks")
	}
	for i, h := range settings.Hooks {
		if h.Event != "before_tool" && h.Event != "after_tool" && h.Event != "session_start" && h.Event != "session_end" && h.Event != "pre_compact" {
			return fmt.Errorf("第 %d 项事件无效", i+1)
		}
		if strings.TrimSpace(h.Tool) != h.Tool || h.Tool == "" || len(h.Tool) > 128 {
			return fmt.Errorf("第 %d 项工具名无效", i+1)
		}
		if strings.TrimSpace(h.Command) != h.Command || !filepath.IsAbs(h.Command) || len(h.Command) > 4096 || strings.ContainsRune(h.Command, 0) {
			return fmt.Errorf("第 %d 项命令须为有效的绝对路径", i+1)
		}
		if h.TimeoutSeconds < 0 || h.TimeoutSeconds > 60 || len(h.Args) > 32 {
			return fmt.Errorf("第 %d 项超时或参数无效", i+1)
		}
		for _, arg := range h.Args {
			if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
				return fmt.Errorf("第 %d 项参数无效", i+1)
			}
		}
	}
	return nil
}

func LoadCommandSettings(dir string) (CommandSettings, error) {
	settings := CommandSettings{}
	if strings.TrimSpace(dir) == "" {
		return CommandSettings{Hooks: []CommandHook{}}, nil
	}
	f, err := os.Open(filepath.Join(dir, "hooks.json"))
	if os.IsNotExist(err) {
		return CommandSettings{Hooks: []CommandHook{}}, nil
	}
	if err != nil {
		return settings, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return settings, err
	}
	if info.Size() > 128*1024 {
		return settings, errors.New("hooks 配置超过 128 KiB")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 128*1024+1))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&settings); err != nil {
		return settings, err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return settings, errors.New("invalid hooks configuration")
	}
	if settings.Hooks == nil {
		return settings, errors.New("hooks 配置必须包含数组")
	}
	return settings, ValidateCommandSettings(settings)
}

func SaveCommandSettings(dir string, settings CommandSettings) error {
	commandSettingsMu.Lock()
	defer commandSettingsMu.Unlock()
	return saveCommandSettings(dir, settings)
}

func SaveCommandSettingsIfUnchanged(dir, revision string, settings CommandSettings) error {
	commandSettingsMu.Lock()
	defer commandSettingsMu.Unlock()
	current, err := LoadCommandSettings(dir)
	if err != nil {
		return err
	}
	if revision == "" || revision != CommandSettingsRevision(current) {
		return ErrCommandSettingsChanged
	}
	return saveCommandSettings(dir, settings)
}

func saveCommandSettings(dir string, settings CommandSettings) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("hooks 数据目录为空")
	}
	if err := ValidateCommandSettings(settings); err != nil {
		return err
	}
	if settings.Hooks == nil {
		settings.Hooks = []CommandHook{}
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 128*1024 {
		return errors.New("hooks configuration too large")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".hooks-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(dir, "hooks.json"))
}

type commandInput struct {
	Event           string          `json:"event"`
	Workspace       string          `json:"workspace"`
	Tool            string          `json:"tool"`
	ToolCallID      string          `json:"tool_call_id"`
	Args            json.RawMessage `json:"args"`
	Result          string          `json:"result,omitempty"`
	ResultTruncated bool            `json:"result_truncated,omitempty"`
	IsError         bool            `json:"is_error,omitempty"`
	Goal            string          `json:"goal,omitempty"`
	Success         *bool           `json:"success,omitempty"`
	ContextTokens   int             `json:"context_tokens,omitempty"`
	ContextWindow   int             `json:"context_window,omitempty"`
	MessageCount    int             `json:"message_count,omitempty"`
}

// CommandObservers are notifications: their failures are logged and do not
// change a task's result or prevent compaction.
func CommandObservers(dir, workspace string) (agent.SessionStartHook, agent.SessionEndHook, agent.PreCompressHook) {
	run := func(ctx context.Context, event string, input commandInput) {
		settings, err := LoadCommandSettings(dir)
		if err != nil {
			slog.Warn("读取 hooks 失败", "event", event, "error", err)
			return
		}
		for _, h := range settings.Hooks {
			if h.Enabled && h.Event == event {
				if err := runCommand(ctx, h, workspace, input); err != nil {
					slog.Warn("hook 执行失败", "event", event, "error", err)
				}
			}
		}
	}
	start := func(ctx context.Context, event agent.SessionStartEvent) error {
		run(ctx, "session_start", commandInput{Event: "session_start", Workspace: workspace, Goal: event.Goal})
		return nil
	}
	end := func(ctx context.Context, event agent.SessionEndEvent) error {
		success := event.Err == nil
		run(ctx, "session_end", commandInput{Event: "session_end", Workspace: workspace, Success: &success})
		return nil
	}
	compact := func(ctx context.Context, event agent.PreCompressEvent) error {
		run(ctx, "pre_compact", commandInput{Event: "pre_compact", Workspace: workspace, ContextTokens: event.ContextTokens, ContextWindow: event.ContextWindow, MessageCount: event.MessageCount})
		return nil
	}
	return start, end, compact
}

type limitedBuffer struct {
	bytes.Buffer
	max      int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remain := b.max - b.Len()
	if remain > 0 {
		if len(p) > remain {
			p = p[:remain]
		}
		_, _ = b.Buffer.Write(p)
	}
	if n > remain {
		b.exceeded = true
	}
	return n, nil
}

func runCommand(ctx context.Context, hook CommandHook, workspace string, input commandInput) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	timeout := hook.TimeoutSeconds
	if timeout == 0 {
		timeout = 10
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, hook.Command, hook.Args...)
	cmd.Dir = workspace
	cmd.Stdin = bytes.NewReader(data)
	output := &limitedBuffer{max: 16 * 1024}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("hook 超时或已取消: %w", ctx.Err())
		}
		return fmt.Errorf("hook 命令执行失败: %w", err)
	}
	if output.exceeded {
		return errors.New("hook 输出超过 16 KiB")
	}
	if input.Event == "before_tool" && len(bytes.TrimSpace(output.Bytes())) > 0 {
		var result struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || (result.Decision != "allow" && result.Decision != "deny") {
			return errors.New("hook 响应必须是 decision 为 allow 或 deny 的 JSON")
		}
		if result.Decision == "deny" {
			reason := strings.TrimSpace(result.Reason)
			if len(reason) > 1024 {
				reason = reason[:1024]
			}
			if reason == "" {
				reason = "被 hook 拒绝"
			}
			return errors.New(reason)
		}
	}
	return nil
}

// CommandHooks reads saved settings on each invocation so a host edit takes
// effect for the next tool call, including already-open sessions.
func CommandHooks(dir, workspace string) (agent.BeforeToolCallHook, agent.AfterToolCallHook) {
	before := func(ctx context.Context, call agent.ToolCallContext) (agent.ToolCallContext, error) {
		settings, err := LoadCommandSettings(dir)
		if err != nil {
			return call, fmt.Errorf("读取 hooks 失败: %w", err)
		}
		for _, h := range settings.Hooks {
			if h.Enabled && h.Event == "before_tool" && (h.Tool == "*" || h.Tool == call.ToolName) {
				if err = runCommand(ctx, h, workspace, commandInput{Event: h.Event, Workspace: workspace, Tool: call.ToolName, ToolCallID: call.ToolCallID, Args: call.Args}); err != nil {
					return call, err
				}
			}
		}
		return call, nil
	}
	after := func(ctx context.Context, call agent.ToolCallContext, result agent.ToolResult) (agent.ToolResult, error) {
		settings, err := LoadCommandSettings(dir)
		if err != nil {
			slog.Warn("读取 after hooks 失败", "error", err)
			return result, nil
		}
		content := result.Content
		truncated := len(content) > 32768
		if truncated {
			content = content[:32768]
		}
		for _, h := range settings.Hooks {
			if h.Enabled && h.Event == "after_tool" && (h.Tool == "*" || h.Tool == call.ToolName) {
				if err = runCommand(ctx, h, workspace, commandInput{Event: h.Event, Workspace: workspace, Tool: call.ToolName, ToolCallID: call.ToolCallID, Args: call.Args, Result: content, ResultTruncated: truncated, IsError: result.IsError}); err != nil {
					slog.Warn("after hook 失败", "tool", call.ToolName, "error", err)
				}
			}
		}
		return result, nil
	}
	return before, after
}
