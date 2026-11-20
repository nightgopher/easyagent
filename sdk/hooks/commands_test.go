package hooks

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/stretchr/testify/require"
)

func TestCommandHookHelper(t *testing.T) {
	args := os.Args
	if len(args) < 3 || args[len(args)-2] != "hook-helper" {
		return
	}
	data, _ := io.ReadAll(os.Stdin)
	switch args[len(args)-1] {
	case "deny":
		_, _ = os.Stdout.WriteString(`{"decision":"deny","reason":"policy denied"}`)
	case "allow":
		_, _ = os.Stdout.WriteString(`{"decision":"allow"}`)
	case "input":
		_ = os.WriteFile(os.Getenv("EASYAGENT_HOOK_TEST_INPUT"), data, 0600)
	case "timeout":
		time.Sleep(5 * time.Second)
	case "fail":
		os.Exit(3)
	}
	os.Exit(0)
}

func TestCommandHooksExecuteAndReload(t *testing.T) {
	dir, workspace := t.TempDir(), t.TempDir()
	output := filepath.Join(dir, "input.json")
	t.Setenv("EASYAGENT_HOOK_TEST_INPUT", output)
	command, err := os.Executable()
	require.NoError(t, err)
	h := CommandHook{Event: "before_tool", Tool: "write", Command: command, Args: []string{"-test.run=^TestCommandHookHelper$", "hook-helper", "deny"}, Enabled: true}
	require.NoError(t, SaveCommandSettings(dir, CommandSettings{Hooks: []CommandHook{h}}))
	before, after := CommandHooks(dir, workspace)
	call := agent.ToolCallContext{ToolName: "write", ToolCallID: "call", Args: json.RawMessage(`{"path":"a"}`)}
	_, err = before(context.Background(), call)
	require.ErrorContains(t, err, "policy denied")
	call.ToolName = "read"
	_, err = before(context.Background(), call)
	require.NoError(t, err)
	h.Event, h.Tool, h.Args[2] = "after_tool", "*", "input"
	require.NoError(t, SaveCommandSettings(dir, CommandSettings{Hooks: []CommandHook{h}}))
	result := agent.ToolResult{Content: strings.Repeat("x", 33000)}
	returned, err := after(context.Background(), call, result)
	require.NoError(t, err)
	require.Equal(t, result.Content, returned.Content)
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	var input commandInput
	require.NoError(t, json.Unmarshal(data, &input))
	require.Equal(t, workspace, input.Workspace)
	require.Equal(t, "read", input.Tool)
	require.True(t, input.ResultTruncated)
	require.Len(t, input.Result, 32768)
	h.Event, h.Args[2], h.TimeoutSeconds = "before_tool", "timeout", 1
	require.NoError(t, SaveCommandSettings(dir, CommandSettings{Hooks: []CommandHook{h}}))
	_, err = before(context.Background(), call)
	require.ErrorContains(t, err, "超时")
	h.Event, h.Args[2], h.TimeoutSeconds = "session_start", "input", 1
	require.NoError(t, SaveCommandSettings(dir, CommandSettings{Hooks: []CommandHook{h}}))
	start, _, _ := CommandObservers(dir, workspace)
	require.NoError(t, start(context.Background(), agent.SessionStartEvent{Goal: "finish"}))
	data, err = os.ReadFile(output)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &input))
	require.Equal(t, "session_start", input.Event)
	require.Equal(t, "finish", input.Goal)
}

func TestCommandSettingsRejectInvalidAndConflictingChanges(t *testing.T) {
	dir := t.TempDir()
	settings, err := LoadCommandSettings(dir)
	require.NoError(t, err)
	rev := CommandSettingsRevision(settings)
	good := CommandSettings{Hooks: []CommandHook{{Event: "before_tool", Tool: "*", Command: filepath.Join(t.TempDir(), "test"), Enabled: true}}}
	require.NoError(t, SaveCommandSettingsIfUnchanged(dir, rev, good))
	require.ErrorIs(t, SaveCommandSettingsIfUnchanged(dir, rev, CommandSettings{}), ErrCommandSettingsChanged)
	bad := good
	bad.Hooks = append([]CommandHook(nil), good.Hooks...)
	bad.Hooks[0].Event = "unknown"
	require.Error(t, SaveCommandSettings(dir, bad))
	bad.Hooks[0].Event = "before_tool"
	bad.Hooks[0].Command = "./project-script"
	require.Error(t, SaveCommandSettings(dir, bad), "a project-controlled relative executable must not be trusted")
	got, err := LoadCommandSettings(dir)
	require.NoError(t, err)
	require.Equal(t, good, got)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(`{}`), 0600))
	before, _ := CommandHooks(dir, dir)
	_, err = before(context.Background(), agent.ToolCallContext{ToolName: "write"})
	require.ErrorContains(t, err, "读取 hooks 失败", "invalid configuration must not silently bypass hooks")
}

func TestUnconfiguredHookDirectoryDoesNotReadProjectFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile("hooks.json", []byte(`{}`), 0600))
	before, _ := CommandHooks("", dir)
	_, err := before(context.Background(), agent.ToolCallContext{ToolName: "write"})
	require.NoError(t, err, "a project-local hooks file is not trusted configuration")
}
