package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nightgopher/easyagent/internal/tui"
)

const cliWatchUnit = "easyagent-cli-watch.service"
const cliWatchMarker = "# Managed by EasyAgent CLI watch settings\n"

var cliWatchPlatform = runtime.GOOS

func checkWatchLinger(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "loginctl", "show-user", strconv.Itoa(os.Getuid()), "--property=Linger", "--value").CombinedOutput()
	if err != nil {
		return fmt.Errorf("无法检查 systemd linger 状态：%w", err)
	}
	if strings.TrimSpace(string(out)) != "yes" {
		return fmt.Errorf("未启用 linger；请执行 loginctl enable-linger <用户名>")
	}
	return nil
}

type cliWatchConfig struct {
	CoreURL  string `json:"core_url"`
	Session  string `json:"session,omitempty"`
	Interval string `json:"interval"`
}

func (b *cliConfigBackend) watchPath() string { return filepath.Join(b.home, "cli-watch.json") }
func (b *cliConfigBackend) loadWatch() (cliWatchConfig, error) {
	c := cliWatchConfig{CoreURL: "http://127.0.0.1:8080", Interval: "30s"}
	data, err := os.ReadFile(b.watchPath())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if len(data) > 4096 {
		return c, fmt.Errorf("看门狗配置过大")
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, nil
}
func (b *cliConfigBackend) WatchSnapshot(ctx context.Context) tui.WatchSnapshot {
	c, err := b.loadWatch()
	if err != nil {
		return tui.WatchSnapshot{Status: "配置读取失败: " + err.Error()}
	}
	s := tui.WatchSnapshot{CoreURL: c.CoreURL, Session: c.Session, Interval: c.Interval, Status: "未启动", ConfigPath: b.watchPath()}
	if s.Session == "" {
		s.Session = "自动发现"
	}
	if cliWatchPlatform != "linux" {
		s.Status = "用户服务目前仅支持 Linux（桌面端可使用飞书设置）"
		return s
	}
	if err := checkWatchLinger(ctx); err != nil {
		s.Warning = err.Error()
	}
	userConfig, err := os.UserConfigDir()
	if err != nil {
		s.Status = "状态不可用: " + err.Error()
		return s
	}
	unitPath := filepath.Join(userConfig, "systemd", "user", cliWatchUnit)
	info, err := os.Lstat(unitPath)
	if os.IsNotExist(err) {
		return s
	}
	if err != nil {
		s.Status = "状态不可用: " + err.Error()
		return s
	}
	if !info.Mode().IsRegular() {
		s.Status = "已有同名服务文件，CLI 未接管"
		return s
	}
	unit, err := os.ReadFile(unitPath) //nolint:gosec // Fixed unit name under the user's systemd config directory.
	if err != nil {
		s.Status = "状态不可用: " + err.Error()
		return s
	}
	if !strings.HasPrefix(string(unit), cliWatchMarker) {
		s.Status = "已有同名服务，CLI 未接管"
		return s
	}
	home, err := filepath.Abs(b.home)
	if err != nil {
		s.Status = "状态不可用: " + err.Error()
		return s
	}
	quotedHome := strconv.Quote(strings.ReplaceAll("EA_HOME="+home, "%", "%%"))
	if !strings.Contains(string(unit), "\nEnvironment="+quotedHome+"\n") {
		s.Status = "看门狗服务属于其他 EA_HOME"
		return s
	}
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "is-active", cliWatchUnit).CombinedOutput()
	if err == nil && strings.TrimSpace(string(out)) == "active" {
		s.Status = "运行中（systemd 用户服务）"
	} else if text := strings.TrimSpace(string(out)); text != "" && text != "inactive" && text != "unknown" && text != "failed" {
		s.Status = "状态不可用: " + text
	} else if strings.TrimSpace(string(out)) == "failed" {
		s.Status = "服务失败；请查看 journalctl --user -u " + cliWatchUnit
	} else if err != nil && strings.TrimSpace(string(out)) == "" {
		s.Status = "状态不可用: " + err.Error()
	}
	return s
}
func (b *cliConfigBackend) SetWatchField(field, value string) error {
	c, err := b.loadWatch()
	if err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	switch field {
	case "url":
		u, e := url.Parse(value)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("请输入不含凭据或查询参数的 HTTP(S) 核心地址")
		}
		c.CoreURL = value
	case "session":
		if strings.ContainsAny(value, "\r\n\x00") || len(value) > 200 {
			return fmt.Errorf("会话 ID 无效")
		}
		c.Session = value
	case "interval":
		d, e := time.ParseDuration(value)
		if e != nil || d < 10*time.Second {
			return fmt.Errorf("轮询间隔至少 10s，例如 30s")
		}
		c.Interval = d.String()
	default:
		return fmt.Errorf("未知配置项")
	}
	if err = os.MkdirAll(b.home, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(b.home, ".cli-watch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), b.watchPath())
}
func (b *cliConfigBackend) SetWatchRunning(ctx context.Context, start bool) error {
	if cliWatchPlatform != "linux" {
		return fmt.Errorf("该入口需要 Linux systemd 用户服务；macOS 请使用桌面端看门狗")
	}
	home, err := filepath.Abs(b.home)
	if err != nil {
		return err
	}
	if strings.ContainsAny(home, "\r\n\x00") {
		return fmt.Errorf("EA_HOME 路径不能包含换行符")
	}
	q := func(s string) string { return strconv.Quote(strings.ReplaceAll(s, "%", "%%")) }
	userConfig, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(userConfig, "systemd", "user")
	unitPath := filepath.Join(unitDir, cliWatchUnit)
	if info, e := os.Lstat(unitPath); e == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("已有非普通文件，未操作: %s", unitPath)
		}
		old, readErr := os.ReadFile(unitPath) //nolint:gosec // Fixed unit name under the user's systemd config directory.
		if readErr != nil {
			return readErr
		}
		if !strings.HasPrefix(string(old), cliWatchMarker) {
			return fmt.Errorf("已有同名用户服务，未操作: %s", unitPath)
		}
		if !strings.Contains(string(old), "\nEnvironment="+q("EA_HOME="+home)+"\n") {
			return fmt.Errorf("该服务属于其他 EA_HOME，未操作: %s", unitPath)
		}
	} else if !os.IsNotExist(e) {
		return e
	} else if !start {
		return fmt.Errorf("没有由 CLI 创建的看门狗服务")
	}
	if !start {
		out, err := exec.CommandContext(ctx, "systemctl", "--user", "disable", "--now", cliWatchUnit).CombinedOutput()
		if err != nil {
			return fmt.Errorf("停止失败: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := checkWatchLinger(ctx); err != nil {
		return err
	}
	c, err := b.loadWatch()
	if err != nil {
		return err
	}
	if c.CoreURL == "" {
		return fmt.Errorf("请先配置核心地址")
	}
	if _, err = os.Stat(b.watchPath()); os.IsNotExist(err) {
		if err = b.SetWatchField("url", c.CoreURL); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err = exec.LookPath("lark-cli"); err != nil {
		return fmt.Errorf("请先安装 lark-cli 并完成用户登录")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(unitDir, 0700); err != nil {
		return err
	}
	// Unit arguments use systemd quoting. No tokens are written into this file.
	unit := cliWatchMarker + "[Unit]\nDescription=EasyAgent CLI Feishu inbox watch\nAfter=network-online.target\n[Service]\nType=simple\nEnvironment=" + q("EA_HOME="+home) + "\nEnvironment=" + q("PATH="+os.Getenv("PATH")) + "\nEnvironmentFile=-" + strings.ReplaceAll(filepath.Join(home, "cli-watch.env"), "%", "%%") + "\nExecStart=" + q(binary) + " watch serve --config " + q(filepath.Join(home, "cli-watch.json")) + " --state-file " + q(filepath.Join(home, "cli-watch-state.json")) + "\nRestart=on-failure\nRestartSec=5\n[Install]\nWantedBy=default.target\n"
	f, err := os.CreateTemp(unitDir, ".easyagent-watch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.WriteString(unit)
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(unitDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.Rename(filepath.Base(f.Name()), cliWatchUnit); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd 重载失败: %s", strings.TrimSpace(string(out)))
	}
	out, err = exec.CommandContext(ctx, "systemctl", "--user", "enable", cliWatchUnit).CombinedOutput()
	if err == nil {
		out, err = exec.CommandContext(ctx, "systemctl", "--user", "restart", cliWatchUnit).CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("启动失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
