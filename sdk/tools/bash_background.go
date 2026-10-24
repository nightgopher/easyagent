package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hwj123hwj/easyagent/sdk/agent"
	"github.com/hwj123hwj/easyagent/sdk/operations"
)

const (
	// maxBackgroundTasks 限制同时运行的后台任务数，避免模型无限启动进程。
	maxBackgroundTasks = 8
	// maxBackgroundOutput 每个任务保留的输出上限（保留末尾），更早的部分丢弃并标注。
	maxBackgroundOutput = 1 << 20
	// maxFinishedTasks 已结束任务保留的条数，供 bash_output 查看结果。
	maxFinishedTasks = 20
)

// liveTasks 记录进程内所有运行中的后台任务，供进程退出时统一终止。
var liveTasks sync.Map // *backgroundTask -> struct{}

// StopBackgroundTasks 终止进程内所有后台 bash 任务（含其进程组），并等待它们退出。
// 应在应用退出前调用：后台进程处于独立会话，父进程退出后不会自动结束。
func StopBackgroundTasks() {
	var tasks []*backgroundTask
	liveTasks.Range(func(k, _ any) bool {
		tasks = append(tasks, k.(*backgroundTask))
		return true
	})
	stopTasks(tasks)
}

func stopTasks(tasks []*backgroundTask) {
	for _, task := range tasks {
		task.kill()
	}
	for _, task := range tasks {
		select {
		case <-task.done:
		case <-time.After(3 * time.Second):
		}
	}
}

// BackgroundTasks 保存一个会话的后台 bash 任务。会话内的 bash、bash_output、bash_kill 共享同一实例。
type BackgroundTasks struct {
	mu     sync.Mutex
	next   int
	tasks  map[string]*backgroundTask
	ledger *orphanLedger
}

type BackgroundTasksOption func(*BackgroundTasks)

// WithBackgroundDataDir enables orphan recovery for local tasks in this registry.
func WithBackgroundDataDir(dataDir string) BackgroundTasksOption {
	return func(tasks *BackgroundTasks) { tasks.ledger = newOrphanLedger(dataDir) }
}

// NewBackgroundTasks creates an empty per-session task registry.
func NewBackgroundTasks(options ...BackgroundTasksOption) *BackgroundTasks {
	tasks := &BackgroundTasks{tasks: map[string]*backgroundTask{}}
	for _, option := range options {
		option(tasks)
	}
	return tasks
}

// Close 终止本会话所有仍在运行的后台任务。
func (b *BackgroundTasks) Close() error {
	b.mu.Lock()
	var tasks []*backgroundTask
	for _, task := range b.tasks {
		tasks = append(tasks, task)
	}
	b.mu.Unlock()
	stopTasks(tasks)
	return nil
}

type backgroundTask struct {
	id      string
	command string
	started time.Time
	cancel  context.CancelFunc
	done    chan struct{}

	mu       sync.Mutex
	pgid     int    // 进程组长 PID（setsid 后即进程组 ID），启动成功后由 OnStart 填入
	output   []byte // 末尾 maxBackgroundOutput 字节
	written  int64  // 累计写入字节数
	readPos  int64  // bash_output 已返回到的累计位置
	killed   bool
	exitCode int
	runErr   error
	finished time.Time
}

func (t *backgroundTask) setPGID(pid int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pgid = pid
}

func (t *backgroundTask) currPGID() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pgid
}

func (t *backgroundTask) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output = append(t.output, p...)
	if over := len(t.output) - maxBackgroundOutput; over > 0 {
		t.output = append(t.output[:0], t.output[over:]...)
	}
	t.written += int64(len(p))
	return len(p), nil
}

func (t *backgroundTask) kill() {
	t.mu.Lock()
	running := t.finished.IsZero()
	if running {
		t.killed = true
	}
	t.mu.Unlock()
	if running {
		t.cancel()
	}
}

func (b *BackgroundTasks) start(ops operations.BashOperations, command, workDir string) (*backgroundTask, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	token := ""
	if b.ledger != nil {
		var err error
		token, err = newBackgroundProcessToken()
		if err != nil {
			return nil, err
		}
	}
	running := 0
	for _, task := range b.tasks {
		if task.status() == "running" {
			running++
		}
	}
	if running >= maxBackgroundTasks {
		return nil, fmt.Errorf("too many background tasks running (%d); stop one with bash_kill first", running)
	}
	marker, err := b.ledger.createMarker(token)
	if err != nil {
		return nil, fmt.Errorf("create background task marker: %w", err)
	}
	b.pruneFinishedLocked()
	b.next++
	ctx, cancel := context.WithCancel(context.Background())
	task := &backgroundTask{
		id:      fmt.Sprintf("bg-%d", b.next),
		command: command,
		started: time.Now(),
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	b.tasks[task.id] = task
	liveTasks.Store(task, struct{}{})
	go func() {
		defer close(task.done)
		defer liveTasks.Delete(task)
		defer cancel()
		defer func() {
			if b.ledger != nil {
				killOrphanProcessGroup(orphanRecord{PGID: task.currPGID(), Token: token})
				b.ledger.remove(task.currPGID(), token)
			}
			if marker != nil {
				marker.Close()
				_ = os.Remove(marker.Name())
			}
		}()
		var recordErr error
		var env []string
		var files []*os.File
		if token != "" {
			env = []string{backgroundProcessTokenEnv + "=" + token}
			files = []*os.File{marker}
		}
		result, err := ops.Run(ctx, operations.RunRequest{
			Command:    command,
			WorkDir:    workDir,
			Output:     task,
			Env:        env,
			ExtraFiles: files,
			// 进程组记入数据目录：easyagent 异常退出后，下次启动据此清理残留进程
			OnStart: func(pid int) {
				task.setPGID(pid)
				recordErr = b.ledger.add(orphanRecord{
					PGID:      pid,
					OwnerPID:  os.Getpid(),
					Token:     token,
					StartedAt: time.Now().Unix(),
				})
				if recordErr != nil {
					cancel()
				}
			},
		})
		if recordErr != nil {
			err = fmt.Errorf("record background task: %w", recordErr)
			result.ExitCode = -1
		}
		if len(result.Output) > 0 { // 不支持流式输出的后端在结束时一次性返回
			_, _ = task.Write(result.Output)
		}
		task.mu.Lock()
		task.exitCode, task.runErr, task.finished = result.ExitCode, err, time.Now()
		task.mu.Unlock()
	}()
	return task, nil
}

func (b *BackgroundTasks) pruneFinishedLocked() {
	var finished []*backgroundTask
	for _, task := range b.tasks {
		if task.status() != "running" {
			finished = append(finished, task)
		}
	}
	if len(finished) < maxFinishedTasks {
		return
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].started.Before(finished[j].started) })
	for _, task := range finished[:len(finished)-maxFinishedTasks+1] {
		delete(b.tasks, task.id)
	}
}

func (b *BackgroundTasks) get(id string) (*backgroundTask, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	task, ok := b.tasks[id]
	return task, ok
}

func (b *BackgroundTasks) list() []*backgroundTask {
	b.mu.Lock()
	defer b.mu.Unlock()
	tasks := make([]*backgroundTask, 0, len(b.tasks))
	for _, task := range b.tasks {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].started.Before(tasks[j].started) })
	return tasks
}

func (t *backgroundTask) status() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.statusLocked()
}

// finishState returns the final exit code and run error; call after done closes.
func (t *backgroundTask) finishState() (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitCode, t.runErr
}

func (t *backgroundTask) statusLocked() string {
	switch {
	case t.finished.IsZero():
		return "running"
	case t.killed:
		return "killed"
	case t.runErr != nil && !errors.Is(t.runErr, context.Canceled):
		return "failed: " + t.runErr.Error()
	default:
		return fmt.Sprintf("exited with code %d", t.exitCode)
	}
}

// readNew 返回自上次读取以来的新输出；被丢弃的部分以提示替代。
func (t *backgroundTask) readNew() (string, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	kept := t.written - int64(len(t.output)) // 缓冲区首字节对应的累计位置
	var note string
	from := t.readPos
	if from < kept {
		note = fmt.Sprintf("[%d bytes of earlier output were dropped; only the last %d bytes are kept]\n", kept-from, maxBackgroundOutput)
		from = kept
	}
	out := string(t.output[from-kept:])
	t.readPos = t.written
	return t.statusLocked(), note + out
}

func (t *backgroundTask) summary() string {
	elapsed := time.Since(t.started)
	t.mu.Lock()
	if !t.finished.IsZero() {
		elapsed = t.finished.Sub(t.started)
	}
	status := t.statusLocked()
	t.mu.Unlock()
	return fmt.Sprintf("%s  %s  %s  %s", t.id, status, elapsed.Round(time.Second), t.command)
}

// BackgroundTaskInfo describes a task for clients that list or stop tasks.
type BackgroundTaskInfo struct {
	ID       string    `json:"id"`
	Command  string    `json:"command"`
	Status   string    `json:"status"`
	Running  bool      `json:"running"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
}

func (t *backgroundTask) info() BackgroundTaskInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return BackgroundTaskInfo{ID: t.id, Command: t.command, Status: t.statusLocked(), Running: t.finished.IsZero(), Started: t.started, Finished: t.finished}
}

// List returns the session's tasks, oldest first.
func (b *BackgroundTasks) List() []BackgroundTaskInfo {
	tasks := b.list()
	out := make([]BackgroundTaskInfo, len(tasks))
	for i, task := range tasks {
		out[i] = task.info()
	}
	return out
}

// Tail returns up to maxBytes of a task's most recent output without moving
// the position bash_output reads from, so viewing it does not hide output
// from the model.
func (b *BackgroundTasks) Tail(id string, maxBytes int) (BackgroundTaskInfo, string, bool) {
	task, ok := b.get(id)
	if !ok {
		return BackgroundTaskInfo{}, "", false
	}
	task.mu.Lock()
	out := task.output
	if maxBytes > 0 && len(out) > maxBytes {
		out = out[len(out)-maxBytes:]
	}
	text := string(out)
	task.mu.Unlock()
	return task.info(), text, true
}

// Stop ends a running task and its process group; false if the ID is unknown.
func (b *BackgroundTasks) Stop(id string) bool {
	task, ok := b.get(id)
	if !ok {
		return false
	}
	task.kill()
	select {
	case <-task.done:
	case <-time.After(5 * time.Second):
	}
	return true
}

// ─── bash_output ─────────────────────────────────────────────────────────────

// BashOutputTool 读取后台任务的新输出；不带 task_id 时列出本会话全部后台任务。
type BashOutputTool struct {
	tasks        *BackgroundTasks
	maxOutputLen int
}

// NewBashOutputTool creates the bash_output tool over a session's task registry.
func NewBashOutputTool(tasks *BackgroundTasks, maxOutputLen int) *BashOutputTool {
	return &BashOutputTool{tasks: tasks, maxOutputLen: maxOutputLen}
}

func (t *BashOutputTool) Name() string { return "bash_output" }
func (t *BashOutputTool) Description() string {
	return "Read new output from a background bash task started with run_in_background. Each call returns only output produced since the previous call, plus the task status. Omit task_id to list all background tasks in this session."
}
func (t *BashOutputTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{"type": "string", "description": "Background task id such as bg-1. Omit to list tasks."},
		},
	}
}
func (t *BashOutputTool) Validate(raw json.RawMessage) (json.RawMessage, error) {
	var params struct {
		TaskID string `json:"task_id"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
	}
	return json.Marshal(params)
}
func (t *BashOutputTool) Execute(_ context.Context, raw json.RawMessage, _ func(agent.PartialResult)) (agent.ToolResult, error) {
	var params struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	if params.TaskID == "" {
		tasks := t.tasks.list()
		if len(tasks) == 0 {
			return agent.ToolResult{Content: "No background tasks in this session."}, nil
		}
		lines := make([]string, len(tasks))
		for i, task := range tasks {
			lines[i] = task.summary()
		}
		return agent.ToolResult{Content: strings.Join(lines, "\n")}, nil
	}
	task, ok := t.tasks.get(params.TaskID)
	if !ok {
		err := fmt.Errorf("unknown background task %q", params.TaskID)
		return agent.ToolResult{IsError: true, Content: err.Error()}, err
	}
	status, out := task.readNew()
	if out == "" {
		out = "(no new output)"
	} else {
		out = TruncateOutput(stripANSI(out), t.maxOutputLen)
	}
	return agent.ToolResult{Content: fmt.Sprintf("%s: %s\n\n%s", task.id, status, out)}, nil
}

// ─── bash_kill ───────────────────────────────────────────────────────────────

// BashKillTool 终止后台任务及其整个进程组。
type BashKillTool struct {
	tasks *BackgroundTasks
}

// NewBashKillTool creates the bash_kill tool over a session's task registry.
func NewBashKillTool(tasks *BackgroundTasks) *BashKillTool {
	return &BashKillTool{tasks: tasks}
}

func (t *BashKillTool) Name() string { return "bash_kill" }
func (t *BashKillTool) Description() string {
	return "Stop a background bash task and every process it started."
}
func (t *BashKillTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id": map[string]any{"type": "string", "description": "Background task id such as bg-1."},
		},
		"required": []string{"task_id"},
	}
}
func (t *BashKillTool) Validate(raw json.RawMessage) (json.RawMessage, error) {
	var params struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if params.TaskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	return json.Marshal(params)
}
func (t *BashKillTool) Execute(ctx context.Context, raw json.RawMessage, _ func(agent.PartialResult)) (agent.ToolResult, error) {
	var params struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	task, ok := t.tasks.get(params.TaskID)
	if !ok {
		err := fmt.Errorf("unknown background task %q", params.TaskID)
		return agent.ToolResult{IsError: true, Content: err.Error()}, err
	}
	if task.status() != "running" {
		return agent.ToolResult{Content: fmt.Sprintf("%s already finished: %s", task.id, task.status())}, nil
	}
	task.kill()
	select {
	case <-task.done:
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		return agent.ToolResult{Content: fmt.Sprintf("%s: kill signal sent, process has not exited yet", task.id)}, nil
	}
	return agent.ToolResult{Content: fmt.Sprintf("%s stopped.", task.id)}, nil
}
