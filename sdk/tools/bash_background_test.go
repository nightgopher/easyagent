//go:build unix

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hwj123hwj/easyagent/sdk/agent"
	"github.com/hwj123hwj/easyagent/sdk/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runTool(t *testing.T, tool agent.Tool, args string) agent.ToolResult {
	t.Helper()
	raw, err := tool.Validate(json.RawMessage(args))
	require.NoError(t, err)
	result, _ := tool.Execute(context.Background(), raw, nil)
	return result
}

func startBackground(t *testing.T, tasks *BackgroundTasks, dir, command string) string {
	t.Helper()
	bash := NewBashTool(WithBashWorkspace(dir), WithBashBackground(tasks))
	args, _ := json.Marshal(map[string]any{"command": command, "run_in_background": true})
	result := runTool(t, bash, string(args))
	require.False(t, result.IsError, result.Content)
	require.Contains(t, result.Content, "bg-")
	return strings.Fields(strings.TrimPrefix(result.Content, "Started background task "))[0][:4]
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBackgroundTaskStartPollKill(t *testing.T) {
	dir := t.TempDir()
	tasks := NewBackgroundTasks()
	t.Cleanup(func() { _ = tasks.Close() })
	output := NewBashOutputTool(tasks, 0)
	kill := NewBashKillTool(tasks)

	id := startBackground(t, tasks, dir, "echo first; sleep 0.2; echo second; sleep 30")
	assert.Equal(t, "bg-1", id)

	waitFor(t, func() bool { return strings.Contains(runTool(t, output, `{"task_id":"bg-1"}`).Content, "second") })
	// Output already returned is not repeated.
	again := runTool(t, output, `{"task_id":"bg-1"}`).Content
	assert.Contains(t, again, "running")
	assert.Contains(t, again, "(no new output)")

	assert.Contains(t, runTool(t, output, `{}`).Content, "bg-1  running")

	killed := runTool(t, kill, `{"task_id":"bg-1"}`)
	assert.False(t, killed.IsError)
	assert.Contains(t, killed.Content, "stopped")
	assert.Contains(t, runTool(t, output, `{"task_id":"bg-1"}`).Content, "killed")
}

func TestBackgroundTaskReportsExitCode(t *testing.T) {
	tasks := NewBackgroundTasks()
	output := NewBashOutputTool(tasks, 0)
	startBackground(t, tasks, t.TempDir(), "echo done; exit 3")
	waitFor(t, func() bool { task, _ := tasks.get("bg-1"); return task.status() != "running" })
	content := runTool(t, output, `{"task_id":"bg-1"}`).Content
	assert.Contains(t, content, "exited with code 3")
	assert.Contains(t, content, "done")
}

func TestBackgroundKillStopsWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	tasks := NewBackgroundTasks()
	startBackground(t, tasks, dir, "sleep 30 & echo $! > child.pid; wait")
	var pid int
	waitFor(t, func() bool {
		data, err := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && pid > 0
	})
	require.NoError(t, tasks.Close())
	waitFor(t, func() bool { return syscall.Kill(pid, 0) != nil })
}

func TestStopBackgroundTasksStopsEverySession(t *testing.T) {
	a, b := NewBackgroundTasks(), NewBackgroundTasks()
	startBackground(t, a, t.TempDir(), "sleep 30")
	startBackground(t, b, t.TempDir(), "sleep 30")
	StopBackgroundTasks()
	for _, tasks := range []*BackgroundTasks{a, b} {
		task, _ := tasks.get("bg-1")
		assert.Equal(t, "killed", task.status())
	}
}

func TestBackgroundOutputKeepsTailAndMarksDrop(t *testing.T) {
	task := &backgroundTask{}
	_, _ = task.Write([]byte(strings.Repeat("a", maxBackgroundOutput)))
	_, _ = task.Write([]byte("tail"))
	_, out := task.readNew()
	assert.Contains(t, out, "4 bytes of earlier output were dropped")
	assert.True(t, strings.HasSuffix(out, "tail"))
	assert.Len(t, task.output, maxBackgroundOutput)
}

func TestBackgroundTaskLimit(t *testing.T) {
	tasks := NewBackgroundTasks()
	t.Cleanup(func() { _ = tasks.Close() })
	for i := 0; i < maxBackgroundTasks; i++ {
		startBackground(t, tasks, t.TempDir(), "sleep 30")
	}
	_, err := tasks.start(operations.LocalBashOperations{}, "sleep 30", t.TempDir())
	assert.ErrorContains(t, err, "too many background tasks")
}

type bufferedBash struct{}

func (bufferedBash) Run(context.Context, operations.RunRequest) (operations.RunResult, error) {
	return operations.RunResult{Output: []byte("buffered")}, nil
}

func TestBackgroundTaskWithNonStreamingBackend(t *testing.T) {
	tasks := NewBackgroundTasks()
	task, err := tasks.start(bufferedBash{}, "x", "")
	require.NoError(t, err)
	<-task.done
	_, out := task.readNew()
	assert.Equal(t, "buffered", out)
}

func TestBashWithoutBackgroundHidesParameter(t *testing.T) {
	props := NewBashTool().Parameters()["properties"].(map[string]any)
	assert.NotContains(t, props, "run_in_background")
	result := runTool(t, NewBashTool(), `{"command":"sleep 1","run_in_background":true}`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "not available")
}
