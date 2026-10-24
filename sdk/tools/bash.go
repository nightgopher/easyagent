package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/operations"
)

const DefaultBashTimeoutSeconds = 120

type BashTool struct {
	workspace        string // 工作目录限制，空字符串表示不限制
	maxOutputLen     int    // 最大输出长度，0 表示使用 DefaultMaxOutputLen
	progressInterval time.Duration
	ops              operations.BashOperations
	background       *BackgroundTasks // nil 表示不支持 run_in_background
}

type BashParams struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
	// RunInBackground 立即返回任务 ID，用 bash_output 读取输出、bash_kill 终止。
	RunInBackground bool `json:"run_in_background,omitempty"`
}

// BashToolOption configures a BashTool during construction.
type BashToolOption func(*BashTool)

// WithBashWorkspace sets the working directory for command execution.
func WithBashWorkspace(ws string) BashToolOption {
	return func(t *BashTool) { t.workspace = ws }
}

// WithBashMaxOutputLen sets the max output truncation length.
func WithBashMaxOutputLen(n int) BashToolOption {
	return func(t *BashTool) { t.maxOutputLen = n }
}

// WithBashOperations sets the BashOperations backend.
func WithBashOperations(ops operations.BashOperations) BashToolOption {
	return func(t *BashTool) { t.ops = ops }
}

// WithBashBackground enables run_in_background with the session's task registry.
func WithBashBackground(tasks *BackgroundTasks) BashToolOption {
	return func(t *BashTool) { t.background = tasks }
}

// NewBashTool creates BashTool with optional configuration.
// If no BashOperations is provided via WithBashOperations, defaults to LocalBashOperations.
func NewBashTool(opts ...BashToolOption) *BashTool {
	t := &BashTool{progressInterval: 5 * time.Second}
	for _, opt := range opts {
		opt(t)
	}
	if t.ops == nil {
		t.ops = operations.LocalBashOperations{}
	}
	return t
}

func (t *BashTool) Name() string { return "bash" }
func (t *BashTool) Description() string {
	return "Execute a non-interactive shell command on the server. No terminal or password input is available; use sudo -n when elevated access is needed."
}
func (t *BashTool) Parameters() map[string]any {
	properties := map[string]any{
		"command": map[string]any{"type": "string", "description": "The shell command to execute."},
	}
	timeoutDesc := "Timeout in seconds (default 120). Set a longer timeout for builds or tests."
	if t.background != nil {
		timeoutDesc = "Timeout in seconds (default 120). A command still running when it expires is moved to a background task (read with bash_output, stop with bash_kill) instead of being killed."
	}
	properties["timeout"] = map[string]any{"type": "integer", "description": timeoutDesc}
	if t.background != nil {
		properties["run_in_background"] = map[string]any{"type": "boolean", "description": "Start the command in the background and return a task id immediately, for dev servers, watchers or long builds. Read output with bash_output and stop it with bash_kill. timeout does not apply."}
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   []string{"command"},
	}
}
func (t *BashTool) Validate(raw json.RawMessage) (json.RawMessage, error) {
	var params BashParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if params.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	if params.Timeout <= 0 {
		params.Timeout = DefaultBashTimeoutSeconds
	}
	return json.Marshal(params)
}

// RequiresConfirmation 实现 agent.ToolWithConfirmation。
// 对破坏性命令（rm -rf、覆盖重定向、sudo、远程脚本执行、磁盘操作等）要求用户确认；
// 普通命令（ls、echo、grep 等）直接放行，避免过度打扰。
func (t *BashTool) RequiresConfirmation(raw json.RawMessage) (string, bool) {
	var params BashParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return "", false // 解析失败交给 Validate 报错，不在此阻断
	}
	if reason := whyDangerous(params.Command); reason != "" {
		return fmt.Sprintf("即将执行 shell 命令（%s）:\n  %s", reason, params.Command), true
	}
	return "", false
}
func (t *BashTool) Execute(ctx context.Context, raw json.RawMessage, onUpdate func(agent.PartialResult)) (agent.ToolResult, error) {
	var params BashParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return agent.ToolResult{IsError: true}, err
	}

	if params.RunInBackground {
		return t.startBackground(params.Command)
	}
	if params.Timeout <= 0 {
		params.Timeout = DefaultBashTimeoutSeconds
	}

	// 有后台任务表时前台执行改为「启动任务 + 等到超时」：超时未完成不杀进程，
	// 移交任务表返回 backgrounded，避免跑了一半的构建/测试作废。sleep 开头的
	// 命令超时后再等也没有产出，维持直接终止。
	if t.background != nil && !backgroundsPoorly(params.Command) {
		return t.executeForegroundTask(ctx, params, onUpdate)
	}
	return t.runForeground(ctx, params, onUpdate)
}

func (t *BashTool) runForeground(ctx context.Context, params BashParams, onUpdate func(agent.PartialResult)) (agent.ToolResult, error) {
	req := operations.RunRequest{
		Command: params.Command,
		Timeout: time.Duration(params.Timeout) * time.Second,
		WorkDir: t.workspace,
	}

	stopProgress := t.reportProgress(ctx, params.Timeout, onUpdate)
	defer stopProgress()
	result, runErr := t.ops.Run(ctx, req)

	output := t.displayOutput(result.Output)

	var failure error
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		if ctx.Err() != nil {
			failure = fmt.Errorf("command interrupted: request deadline exceeded: %w", runErr)
		} else {
			failure = fmt.Errorf("command timed out after %s: %w", req.Timeout, runErr)
			if t.background != nil {
				failure = fmt.Errorf("%w; run long-lived commands with run_in_background", failure)
			}
		}
	case errors.Is(runErr, context.Canceled):
		failure = fmt.Errorf("command canceled: %w", runErr)
	case runErr != nil:
		failure = runErr
	case result.ExitCode != 0:
		failure = fmt.Errorf("command exited with code %d", result.ExitCode)
	}
	if failure != nil {
		if output != "" {
			output += "\n\n"
		}
		output += failure.Error()
		return agent.ToolResult{Content: output, IsError: true}, failure
	}

	return agent.ToolResult{Content: output}, nil
}

// executeForegroundTask runs the command as a registry task and waits up to
// params.Timeout: finished in time reports like a normal foreground run; a
// still-running command is handed to the background table instead of killed.
// User cancellation still stops the process immediately.
func (t *BashTool) executeForegroundTask(ctx context.Context, params BashParams, onUpdate func(agent.PartialResult)) (agent.ToolResult, error) {
	task, err := t.background.start(t.ops, params.Command, t.workspace)
	if err != nil {
		// 注册表已满等启动前失败：退回杀死式超时，命令仍然执行一次。
		return t.runForeground(ctx, params, onUpdate)
	}

	stopProgress := t.reportProgress(ctx, params.Timeout, onUpdate)
	defer stopProgress()

	timer := time.NewTimer(time.Duration(params.Timeout) * time.Second)
	defer timer.Stop()
	select {
	case <-task.done:
		return t.finishedTaskResult(task)
	case <-timer.C:
		return t.backgroundedResult(task, params.Timeout)
	case <-ctx.Done():
		task.kill()
		select {
		case <-task.done:
		case <-time.After(3 * time.Second):
		}
		return t.interruptedTaskResult(task, ctx.Err())
	}
}

func (t *BashTool) finishedTaskResult(task *backgroundTask) (agent.ToolResult, error) {
	_, out := task.readNew()
	output := t.displayOutput([]byte(out))
	exitCode, runErr := task.finishState()

	var failure error
	switch {
	case errors.Is(runErr, context.Canceled):
		failure = fmt.Errorf("command canceled")
	case runErr != nil:
		failure = runErr
	case exitCode != 0:
		failure = fmt.Errorf("command exited with code %d", exitCode)
	}
	if failure != nil {
		if output != "" {
			output += "\n\n"
		}
		output += failure.Error()
		return agent.ToolResult{Content: output, IsError: true}, failure
	}
	return agent.ToolResult{Content: output}, nil
}

func (t *BashTool) backgroundedResult(task *backgroundTask, timeoutSeconds int) (agent.ToolResult, error) {
	// 读走并展示已有输出：readPos 前移后，bash_output 只返回新输出，不重复。
	_, out := task.readNew()
	content := fmt.Sprintf("Command timed out after %ds and was moved to background task %s instead of being killed.\n", timeoutSeconds, task.id)
	if output := t.displayOutput([]byte(out)); output != "" {
		content += "\nOutput so far:\n" + output + "\n"
	}
	content += fmt.Sprintf("\nRead new output with bash_output (task_id %q); stop it with bash_kill.", task.id)
	return agent.ToolResult{Content: content}, nil
}

func (t *BashTool) interruptedTaskResult(task *backgroundTask, cause error) (agent.ToolResult, error) {
	var failure error
	if errors.Is(cause, context.DeadlineExceeded) {
		failure = fmt.Errorf("command interrupted: request deadline exceeded")
	} else {
		failure = fmt.Errorf("command canceled")
	}
	_, out := task.readNew()
	output := t.displayOutput([]byte(out))
	if output != "" {
		output += "\n\n"
	}
	output += failure.Error()
	return agent.ToolResult{Content: output, IsError: true}, failure
}

// displayOutput applies the foreground output rules to raw command output.
func (t *BashTool) displayOutput(raw []byte) string {
	if isBinaryOutput(string(raw)) {
		return fmt.Sprintf("Command produced binary output (%d bytes). Use file redirection to save output.", len(raw))
	}
	return TruncateOutput(stripANSI(string(raw)), t.maxOutputLen)
}

// sleepPrefix 只匹配以 sleep 开头的命令：这类命令超时后没有继续等待的价值。
var sleepPrefix = regexp.MustCompile(`^\s*sleep\b`)

func backgroundsPoorly(command string) bool {
	return sleepPrefix.MatchString(command)
}

func (t *BashTool) startBackground(command string) (agent.ToolResult, error) {
	if t.background == nil {
		err := fmt.Errorf("background tasks are not available in this session")
		return agent.ToolResult{IsError: true, Content: err.Error()}, err
	}
	task, err := t.background.start(t.ops, command, t.workspace)
	if err != nil {
		return agent.ToolResult{IsError: true, Content: err.Error()}, err
	}
	return agent.ToolResult{Content: fmt.Sprintf("Started background task %s. Read its output with bash_output and stop it with bash_kill.", task.id)}, nil
}

// ansiRegex matches ANSI escape sequences.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// stripANSI removes ANSI escape sequences from a string.
func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// isBinaryOutput checks if the output appears to be binary data.
func isBinaryOutput(s string) bool {
	// Check for null bytes in first 512 bytes
	checkLen := len(s)
	if checkLen > 512 {
		checkLen = 512
	}
	for i := 0; i < checkLen; i++ {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

// Even silent commands report that they are still waiting; this is elapsed time,
// not invented output or a completion estimate. Stop and join before tool_end.
func (t *BashTool) reportProgress(ctx context.Context, timeout int, update func(agent.PartialResult)) func() {
	if update == nil {
		return func() {}
	}
	interval := t.progressInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				message := fmt.Sprintf("命令仍在执行 · 已等待 %ds", int(time.Since(start).Seconds()))
				if timeout > 0 {
					message += fmt.Sprintf(" · 本次超时 %ds", timeout)
				}
				update(agent.PartialResult{Content: message})
			}
		}
	}()
	return func() { close(stop); <-done }
}
