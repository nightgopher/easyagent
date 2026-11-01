package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/nightgopher/easyagent/sdk/config"
)

// logFilePerm keeps diagnostics private to the user (gosec G302); the log can
// carry prompt fragments and tool errors, so it must not be group/world read.
const logFilePerm = 0o600

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
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return func() {}, fmt.Errorf("create log dir: %w", err)
	}

	logPath := filepath.Join(logDir, "easyagent.log")
	oldPath := logPath + ".old"
	if fileExists(oldPath) {
		if err := os.Remove(oldPath); err != nil {
			return func() {}, fmt.Errorf("rotate old log: %w", err)
		}
	}
	if fileExists(logPath) {
		if err := os.Rename(logPath, oldPath); err != nil {
			return func() {}, fmt.Errorf("rotate log: %w", err)
		}
	}

	// Opening through os.Root with a literal file name confines symlink
	// traversal and keeps gosec's file-inclusion rule (G304) satisfied.
	root, err := os.OpenRoot(logDir)
	if err != nil {
		return func() {}, fmt.Errorf("open log dir: %w", err)
	}
	f, err := root.OpenFile("easyagent.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFilePerm)
	if err != nil {
		_ = root.Close()
		return func() {}, fmt.Errorf("open log file: %w", err)
	}

	cleanup := sync.OnceFunc(func() {
		_ = f.Close()
		_ = root.Close()
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return cleanup, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
