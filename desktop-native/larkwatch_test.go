package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func withLarkLookup(t *testing.T, lookPath func(string) (string, error)) {
	t.Helper()
	origLook, origStat := larkLookPath, larkStat
	larkLookPath = lookPath
	t.Cleanup(func() { larkLookPath, larkStat = origLook, origStat })
}

func TestResolveLarkCLIPrefersPath(t *testing.T) {
	withLarkLookup(t, func(string) (string, error) { return "/usr/bin/lark-cli", nil })
	if got := resolveLarkCLI(t.TempDir()); got != "/usr/bin/lark-cli" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveLarkCLIFallsBackToInstallLocations(t *testing.T) {
	withLarkLookup(t, func(string) (string, error) { return "", errors.New("not found") })
	home := t.TempDir()
	npm := filepath.Join(home, ".npm-global", "bin", "lark-cli")
	if err := os.MkdirAll(filepath.Dir(npm), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(npm, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveLarkCLI(home); got != npm {
		t.Fatalf("got %q", got)
	}
}

func TestResolveLarkCLIMissing(t *testing.T) {
	withLarkLookup(t, func(string) (string, error) { return "", errors.New("not found") })
	if got := resolveLarkCLI(t.TempDir()); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestLarkWatchStatusAndStop(t *testing.T) {
	m := &localBackend{}
	if _, running, _ := m.LarkWatchStatus(); running {
		t.Fatal("want not running without process")
	}
	stderr := newRingBuffer(512)
	cmd := exec.Command("sh", "-c", "echo watch failed >&2; sleep 30")
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	m.mu.Lock()
	m.watchCmd, m.watchDone, m.watchStderr = cmd, done, stderr
	m.mu.Unlock()

	if _, running, _ := m.LarkWatchStatus(); !running {
		t.Fatal("want running with live process")
	}
	// 进程自行退出（如授权缺失）：watchCmd 未清理，状态展示 stderr 尾部。
	time.Sleep(100 * time.Millisecond) // 等 stderr 写入 ring buffer
	_ = cmd.Process.Kill()
	<-done
	if _, running, detail := m.LarkWatchStatus(); running || detail == "" {
		t.Fatalf("want exited with stderr tail, got running=%v detail=%q", running, detail)
	}
	// 手动停止：清理字段，状态干净无尾部。
	m.stopLarkWatch()
	if _, running, detail := m.LarkWatchStatus(); running || detail != "" {
		t.Fatalf("want clean stop, got running=%v detail=%q", running, detail)
	}
}

func TestStopLarkWatchForceKillsOnHang(t *testing.T) {
	m := &localBackend{}
	cmd := exec.Command("sh", "-c", "trap '' INT; sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	m.mu.Lock()
	m.watchCmd, m.watchDone = cmd, done
	m.mu.Unlock()

	started := time.Now()
	m.stopLarkWatch()
	if time.Since(started) > watchStopGrace+5*time.Second {
		t.Fatalf("stop took too long: %v", time.Since(started))
	}
	if _, running, _ := m.LarkWatchStatus(); running {
		t.Fatal("want stopped after force kill")
	}
}
