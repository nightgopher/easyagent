package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUILoggingWritesPrivateFileWithoutTerminalOutput(t *testing.T) {
	for _, mode := range []string{"chat", "interactive"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("EA_HOME", home)
			previous := slog.Default()
			t.Cleanup(func() { slog.SetDefault(previous) })
			var terminal bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&terminal, nil)))
			closeLog, err := setupLogging(mode)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(closeLog)
			slog.Info("routine-noise")
			slog.Warn("useful-diagnostic", "path", "fixture/SKILL.md")
			closeLog()
			closeLog() // Cleanup is safe even after an earlier shutdown.
			if terminal.Len() != 0 {
				t.Fatalf("logging leaked to terminal: %s", terminal.String())
			}
			path := filepath.Join(home, "logs", "easyagent.log")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "useful-diagnostic") || strings.Contains(string(data), "routine-noise") {
				t.Fatalf("wrong diagnostics: %s", data)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("log must be private: info=%v error=%v", info, err)
			}
			closeNext, err := setupLogging(mode)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(closeNext)
			slog.Warn("next-run")
			closeNext()
			old, err := os.ReadFile(path + ".old")
			if err != nil || !bytes.Equal(old, data) {
				t.Fatalf("previous diagnostics were not retained: %s, %v", old, err)
			}
			current, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(current), "next-run") || bytes.Contains(current, data) {
				t.Fatalf("new run did not use a fresh log: %s, %v", current, err)
			}
		})
	}
}

func TestNonUILoggingKeepsDestinationAndCreatesNoFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EA_HOME", home)
	previous := slog.Default()
	for _, mode := range []string{"run", "serve", "unknown"} {
		closeLog, err := setupLogging(mode)
		if err != nil || slog.Default() != previous {
			t.Fatalf("%s changed logging: %v", mode, err)
		}
		closeLog()
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("non-UI logging touched the filesystem: %v %v", entries, err)
	}
}

func TestUILoggingFailureKeepsExistingLogger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "logs"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := slog.Default()
	closeLog, err := setupLogging("chat")
	if err == nil || slog.Default() != previous {
		t.Fatalf("failed initialization changed logger: %v", err)
	}
	closeLog()
	closeLog()
}
