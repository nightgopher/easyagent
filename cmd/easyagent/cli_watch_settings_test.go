package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLIWatchConfiguration(t *testing.T) {
	b := &cliConfigBackend{home: t.TempDir()}
	require.NoError(t, b.SetWatchField("url", "https://core.example:8080"))
	require.NoError(t, b.SetWatchField("session", "sess_example"))
	require.NoError(t, b.SetWatchField("interval", "15s"))
	require.Error(t, b.SetWatchField("url", "https://secret@example.com"))
	require.Error(t, b.SetWatchField("interval", "1s"))
	c, err := b.loadWatch()
	require.NoError(t, err)
	require.Equal(t, "https://core.example:8080", c.CoreURL)
	require.Equal(t, "sess_example", c.Session)
	require.Equal(t, "15s", c.Interval)
	info, err := os.Stat(b.watchPath())
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0077)
}
func TestCLIWatchUserService(t *testing.T) {
	oldPlatform := cliWatchPlatform
	cliWatchPlatform = "linux"
	t.Cleanup(func() { cliWatchPlatform = oldPlatform })
	temp := t.TempDir()
	t.Setenv("HOME", temp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(temp, "config"))
	t.Setenv("PATH", temp)
	log := filepath.Join(temp, "calls")
	t.Setenv("TEST_WATCH_LOG", log)
	require.NoError(t, os.WriteFile(filepath.Join(temp, "lark-cli"), []byte("#!/bin/sh\nexit 0\n"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(temp, "systemctl"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_WATCH_LOG\"\nexit 0\n"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(temp, "loginctl"), []byte("#!/bin/sh\nprintf '%s\\n' \"$TEST_LINGER\"\n"), 0700))
	b := &cliConfigBackend{home: filepath.Join(temp, "home")}
	t.Setenv("TEST_LINGER", "no")
	require.ErrorContains(t, b.SetWatchRunning(context.Background(), true), "未启用 linger")
	require.Contains(t, b.WatchSnapshot(context.Background()).Warning, "未启用 linger")
	require.NoFileExists(t, log)
	t.Setenv("TEST_LINGER", "yes")
	require.NoError(t, b.SetWatchRunning(context.Background(), true))
	c, err := b.loadWatch()
	require.NoError(t, err)
	require.Equal(t, "30s", c.Interval)
	userConfig, err := os.UserConfigDir()
	require.NoError(t, err)
	unit := filepath.Join(userConfig, "systemd", "user", cliWatchUnit)
	data, err := os.ReadFile(unit)
	require.NoError(t, err)
	require.Contains(t, string(data), "watch serve --config")
	require.Contains(t, string(data), "Restart=on-failure")
	require.Contains(t, string(data), "EnvironmentFile=-"+filepath.Join(temp, "home", "cli-watch.env")+"\n")
	other := &cliConfigBackend{home: filepath.Join(temp, "another-home")}
	require.ErrorContains(t, other.SetWatchRunning(context.Background(), true), "其他 EA_HOME")
	require.ErrorContains(t, other.SetWatchRunning(context.Background(), false), "其他 EA_HOME")
	require.NoError(t, b.SetWatchRunning(context.Background(), false))
	calls, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Contains(t, string(calls), "--user restart "+cliWatchUnit)
	require.Contains(t, string(calls), "--user disable --now "+cliWatchUnit)
	require.NoError(t, os.WriteFile(unit, []byte("unrelated"), 0600))
	require.Error(t, b.SetWatchRunning(context.Background(), false))
	require.Error(t, b.SetWatchRunning(context.Background(), true))
	data, err = os.ReadFile(unit)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "unrelated"))
}

func TestWatchFilePrecedence(t *testing.T) {
	b := &cliConfigBackend{home: t.TempDir()}
	require.NoError(t, b.SetWatchField("url", "http://localhost:1111"))
	require.NoError(t, b.SetWatchField("session", "saved-session"))
	opts := &watchOptions{configFile: b.watchPath()}
	env := map[string]string{"EA_LARKWATCH_CORE_URL": "http://localhost:2222"}
	require.NoError(t, applyWatchSettings(opts, func(key string) string { return env[key] }))
	require.Empty(t, opts.coreURL)
	require.Equal(t, "saved-session", opts.sessionID)
	cfg, err := buildWatchConfig(opts, func(key string) string { return env[key] })
	require.NoError(t, err)
	require.Equal(t, "http://localhost:2222", cfg.CoreURL)
	require.Equal(t, "saved-session", cfg.SessionID)
}

func TestWatchGroupIDsFromFlagAndEnvironment(t *testing.T) {
	env := map[string]string{"EA_LARKWATCH_CORE_URL": "http://localhost:8080", "EA_LARKWATCH_SESSION_ID": "sess_test", "EA_LARKWATCH_GROUP_CHATS": "oc_env"}
	opts, err := parseWatchArgs([]string{"serve", "--group-chats", "oc_one, oc_two,oc_one"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := buildWatchConfig(opts, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.GroupChats) != 2 || !cfg.GroupChats["oc_one"] || !cfg.GroupChats["oc_two"] || cfg.GroupChats["oc_env"] {
		t.Fatalf("groups: %v", cfg.GroupChats)
	}
	opts.groupIDs = ""
	cfg, err = buildWatchConfig(opts, func(k string) string { return env[k] })
	if err != nil || !cfg.GroupChats["oc_env"] {
		t.Fatalf("environment groups: %v, %v", cfg.GroupChats, err)
	}
}
