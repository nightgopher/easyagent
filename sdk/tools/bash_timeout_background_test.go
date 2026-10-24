//go:build unix

package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/hwj123hwj/easyagent/sdk/agent"
	"github.com/hwj123hwj/easyagent/sdk/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func executeBash(t *testing.T, tool agent.Tool, args string) agent.ToolResult {
	t.Helper()
	raw, err := tool.Validate(json.RawMessage(args))
	require.NoError(t, err)
	result, _ := tool.Execute(context.Background(), raw, nil)
	return result
}

// A foreground command that outlives its timeout is handed to the background
// table: output stays continuous, the exit code is reported later, bash_kill works.
func TestForegroundTimeoutMovesToBackgroundTask(t *testing.T) {
	dir := t.TempDir()
	tasks := NewBackgroundTasks()
	t.Cleanup(func() { _ = tasks.Close() })
	bash := NewBashTool(WithBashWorkspace(dir), WithBashBackground(tasks))
	output := NewBashOutputTool(tasks, 0)
	kill := NewBashKillTool(tasks)

	command := "echo running; for i in 1 2 3 4 5; do sleep 1; echo tick $i; done"
	raw, err := bash.Validate(json.RawMessage(`{"command": "` + command + `", "timeout": 1}`))
	require.NoError(t, err)
	result, err := bash.Execute(context.Background(), raw, nil)
	require.NoError(t, err)
	assert.False(t, result.IsError, result.Content)
	assert.Contains(t, result.Content, "moved to background task bg-1")
	assert.Contains(t, result.Content, "running") // 输出连续：超时前已产生的内容不丢

	// bash_output 只返回新输出，不重复已展示的 "running"（状态行的 running 除外）。
	time.Sleep(1500 * time.Millisecond)
	poll := executeBash(t, output, `{"task_id":"bg-1"}`)
	assert.Contains(t, poll.Content, "tick 1")
	assert.NotContains(t, poll.Content, "running\ntick")

	// bash_kill 可停止移交后的任务。
	stopped := executeBash(t, kill, `{"task_id":"bg-1"}`)
	assert.Contains(t, stopped.Content, "bg-1 stopped.")
}

// A command that finishes within the timeout reports output and exit code like
// the old foreground path, with no background noise.
func TestForegroundTaskFinishesWithinTimeout(t *testing.T) {
	dir := t.TempDir()
	tasks := NewBackgroundTasks()
	t.Cleanup(func() { _ = tasks.Close() })
	bash := NewBashTool(WithBashWorkspace(dir), WithBashBackground(tasks))

	result := executeBash(t, bash, `{"command": "echo ok", "timeout": 10}`)
	assert.False(t, result.IsError, result.Content)
	assert.Contains(t, result.Content, "ok")
	assert.NotContains(t, result.Content, "background")

	failing := executeBash(t, bash, `{"command": "sh -c 'echo boom >&2; exit 3'", "timeout": 10}`)
	assert.True(t, failing.IsError, failing.Content)
	assert.Contains(t, failing.Content, "boom")
	assert.Contains(t, failing.Content, "exited with code 3")
}

// sleep 开头的命令超时后直接终止，不占用后台任务位。
func TestSleepCommandTimesOutWithoutBackgrounding(t *testing.T) {
	dir := t.TempDir()
	tasks := NewBackgroundTasks()
	t.Cleanup(func() { _ = tasks.Close() })
	bash := NewBashTool(WithBashWorkspace(dir), WithBashBackground(tasks))

	result := executeBash(t, bash, `{"command": "sleep 10", "timeout": 1}`)
	assert.True(t, result.IsError, result.Content)
	assert.Contains(t, result.Content, "timed out")
	assert.NotContains(t, result.Content, "background task")
	assert.Empty(t, tasks.List(), "no task should be registered for sleep")
}

// User cancellation still stops the process immediately instead of backgrounding.
func TestForegroundTaskCancelKillsImmediately(t *testing.T) {
	dir := t.TempDir()
	tasks := NewBackgroundTasks()
	t.Cleanup(func() { _ = tasks.Close() })
	bash := NewBashTool(WithBashWorkspace(dir), WithBashBackground(tasks))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	raw, err := bash.Validate(json.RawMessage(`{"command": "echo go; sleep 30", "timeout": 60}`))
	require.NoError(t, err)
	result, err := bash.Execute(ctx, raw, nil)
	require.Error(t, err)
	assert.True(t, result.IsError, result.Content)
	assert.Contains(t, result.Content, "canceled")
	waitFor(t, func() bool { return !tasks.List()[0].Running })
}

type holdOperations struct{}

func (holdOperations) Run(ctx context.Context, _ operations.RunRequest) (operations.RunResult, error) {
	<-ctx.Done()
	return operations.RunResult{}, ctx.Err()
}

type deadlineOperations struct {
	mu       sync.Mutex
	requests []operations.RunRequest
}

func (d *deadlineOperations) Run(ctx context.Context, req operations.RunRequest) (operations.RunResult, error) {
	d.mu.Lock()
	d.requests = append(d.requests, req)
	d.mu.Unlock()
	select {
	case <-time.After(req.Timeout):
		return operations.RunResult{}, context.DeadlineExceeded
	case <-ctx.Done():
		return operations.RunResult{}, ctx.Err()
	}
}

// 注册表占满时退回杀死式超时：命令仍然执行一次，Timeout 仍传给后端。
func TestForegroundFallsBackWhenRegistryFull(t *testing.T) {
	tasks := NewBackgroundTasks()
	t.Cleanup(func() { _ = tasks.Close() })
	hold := holdOperations{}
	for i := 0; i < maxBackgroundTasks; i++ {
		_, err := tasks.start(hold, "hold", "")
		require.NoError(t, err)
	}

	ops := &deadlineOperations{}
	bash := NewBashTool(WithBashOperations(ops), WithBashBackground(tasks))
	result := executeBash(t, bash, `{"command": "build", "timeout": 1}`)
	assert.True(t, result.IsError, result.Content)
	assert.Contains(t, result.Content, "timed out after 1s")
	require.Len(t, ops.requests, 1)
	assert.Equal(t, time.Second, ops.requests[0].Timeout)
}
