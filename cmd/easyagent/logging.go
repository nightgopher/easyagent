package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/nightgopher/easyagent/sdk/config"
)

// uiModes run a full-screen (AltScreen) TUI that owns the terminal. Log output
// written to stderr while such a UI is active is interleaved with redraws and
// corrupts the display, so those modes route diagnostics to a log file instead.
var uiModes = map[string]bool{
	"interactive": true,
	"chat":        true,
}

// setupLogging picks the slog destination for the selected mode.
//
// UI modes write to <EA_HOME>/logs/easyagent.log (created on demand, rotated
// at startup by renaming an existing file to the .old suffix), keeping stderr
// clean for the TUI. Other modes keep the default stderr stream; serve mode
// sets up its own file logging, so it also skips the file redirect here.
func setupLogging(mode string) (func(), error) {
	if !uiModes[mode] || mode == "serve" {
		return func() {}, nil
	}

	logDir := filepath.Join(config.HomeDir(), "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return func() {}, fmt.Errorf("create log dir: %w", err)
	}

	logPath := filepath.Join(logDir, "easyagent.log")
	oldPath := logPath + ".old"
	if fileExists(oldPath) {
		os.Remove(oldPath)
	}
	if fileExists(logPath) {
		os.Rename(logPath, oldPath)
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return func() {}, fmt.Errorf("open log file: %w", err)
	}

	cleanup := sync.OnceFunc(func() {
		f.Close()
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return cleanup, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
