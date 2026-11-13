package main

// 桌面托管的 lark 看门狗：随核心拉起 `watch serve` 子进程。核心地址与令牌
// 每次启动都会轮换，托管进程由桌面在核心就绪后用当次值直接注入，核心重启
// 时随 stop/start 一并重启，不存在地址漂移；唤醒目标会话由子进程按 EA_HOME
// 下的 feishu-routes.json 自动发现（恰好一个 owner 会话即生效）。
//
// lark-cli 由看门狗按用户身份轮询飞书，GUI 启动的进程没有 shell PATH，因此
// 这里按常见安装位置解析出绝对路径传给 EA_LARKWATCH_LARK_CLI。

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// watchStopGrace 是停止看门狗进程的宽限期，超时强杀。
const watchStopGrace = 5 * time.Second

// larkLookPath/larkStat 抽成变量供测试注入。
var (
	larkLookPath = exec.LookPath
	larkStat     = func(path string) bool { _, err := os.Stat(path); return err == nil }
)

// resolveLarkCLI 按 PATH 与常见用户级安装位置解析 lark-cli 绝对路径。
func resolveLarkCLI(home string) string {
	if path, err := larkLookPath("lark-cli"); err == nil {
		return path
	}
	for _, candidate := range []string{
		filepath.Join(home, ".npm-global", "bin", "lark-cli"),
		filepath.Join(home, ".local", "bin", "lark-cli"),
		"/opt/homebrew/bin/lark-cli",
		"/usr/local/bin/lark-cli",
	} {
		if larkStat(candidate) {
			return candidate
		}
	}
	return ""
}

// ApplyLarkWatch 应用看门狗开关：保存偏好并在核心在线时立即生效。
// 拉起失败不回滚开关，错误经状态接口在设置页展示。
func (m *localBackend) ApplyLarkWatch(on bool) {
	m.mu.Lock()
	m.watchOn = on
	err := error(nil)
	if m.client != nil {
		if on {
			err = m.startLarkWatchLocked()
		} else {
			m.stopLarkWatchLocked()
		}
	}
	m.mu.Unlock()
	if err != nil {
		m.mu.Lock()
		m.watchErr = err.Error()
		m.mu.Unlock()
	}
}

// startLarkWatchLocked 在核心就绪后拉起看门狗子进程；重复调用先停旧进程。
// 调用方需持有 m.mu 且 m.client 已就绪。
func (m *localBackend) startLarkWatchLocked() error {
	m.watchErr = ""
	m.stopLarkWatchLocked()
	if m.client == nil {
		return nil
	}
	binary, err := coreBinary()
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	larkCLI := resolveLarkCLI(home)
	if larkCLI == "" {
		return errors.New("未找到 lark-cli，请先安装并完成 lark-cli auth login")
	}
	env := map[string]string{
		"EA_LARKWATCH_CORE_URL": m.client.URL,
		"EA_SERVER_API_KEY":     m.client.Token,
		"EA_HOME":               filepath.Join(m.dir, "core-home"),
		"EA_LARKWATCH_LARK_CLI": larkCLI,
		"PATH":                  filepath.Dir(larkCLI) + ":" + os.Getenv("PATH"),
	}
	stderr := newRingBuffer(8192)
	cmd := exec.Command(binary, "watch", "serve") //nolint:gosec // 与核心 helper 同源：Bin 为本机装配的核心，参数固定
	cmd.Env = filterEnvironment(env)
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	m.watchCmd, m.watchDone, m.watchStderr = cmd, done, stderr
	return nil
}

// stopLarkWatch 终止看门狗子进程；无进程时是空操作。
func (m *localBackend) stopLarkWatch() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLarkWatchLocked()
}

func (m *localBackend) stopLarkWatchLocked() {
	if m.watchCmd == nil {
		return
	}
	cmd, done := m.watchCmd, m.watchDone
	m.watchCmd, m.watchDone, m.watchStderr = nil, nil, nil
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(watchStopGrace):
		_ = cmd.Process.Kill()
		<-done
	}
}

// LarkWatchStatus 报告看门狗开关、运行状态与最近的错误输出尾部。
func (m *localBackend) LarkWatchStatus() (on, running bool, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	on = m.watchOn
	if m.watchCmd == nil {
		detail = m.watchErr
		return on, false, detail
	}
	select {
	case <-m.watchDone:
		if m.watchStderr != nil {
			detail = m.watchStderr.String()
		}
		return on, false, detail
	default:
		return on, true, ""
	}
}
