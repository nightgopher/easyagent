package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"
	"github.com/egoist/mygo/ui"
)

// Codex 登录入口：与 EasyCode 同一套 helper 管线（spawn coreBinary，
// --machine NDJSON 事件，core 负责回调与私有凭据文件），但不接管
// connections.json 的模型配置——登录后在会话里用 /models 选择 Codex 模型。

type nativeCodexEvent struct {
	Type     string `json:"type"`
	URL      string `json:"url"`
	LoggedIn bool   `json:"logged_in"`
	User     struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"user"`
	Models []nativeEasyCodeModel `json:"models"`
}
type nativeCodexState struct {
	LoggedIn   bool
	Name, URL  string
	Cancel     context.CancelFunc
	Generation uint64
}

var openCodexBrowser = func(raw string) error { return mygo.Shell.OpenExternal(raw) }
var invokeCodexHelper = runCodexHelper

func codexUserName(event nativeCodexEvent) string {
	if event.User.Name != "" {
		return event.User.Name
	}
	if event.User.Email != "" {
		return event.User.Email
	}
	return "当前账号"
}

// codexLoginURL 校验登录页地址：必须是 OpenAI/ChatGPT 官方域名的 https 链接，
// 防止 helper 输出被劫持到钓鱼页。
func codexLoginURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return "", errors.New("登录程序返回了无效的登录地址")
	}
	host := strings.ToLower(u.Hostname())
	if host != "auth.openai.com" && host != "chatgpt.com" &&
		!strings.HasSuffix(host, ".openai.com") && !strings.HasSuffix(host, ".chatgpt.com") {
		return "", errors.New("登录程序返回了非官方登录地址")
	}
	return raw, nil
}

func runCodexHelper(ctx context.Context, dir, action string, onBrowser func(string)) (nativeCodexEvent, error) {
	var result nativeCodexEvent
	binary, err := coreBinary()
	if err != nil {
		return result, err
	}
	cmd := exec.CommandContext(ctx, binary, "codex", action, "--machine") //nolint:gosec // 与 EasyCode helper 同源：Bin 为本机装配的核心，参数固定
	overrides := map[string]string{"EA_HOME": filepath.Join(dir, "core-home")}
	if u := codexProxyURL(); u != "" {
		overrides["HTTPS_PROXY"] = u
		overrides["HTTP_PROXY"] = u
	}
	cmd.Env = filterEnvironment(overrides)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return result, errors.New("无法启动 Codex 登录")
	}
	stderr := newRingBuffer(4096)
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		return result, errors.New("无法启动 Codex 登录")
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var event nativeCodexEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Type == "browser" {
			loginURL, urlErr := codexLoginURL(event.URL)
			if urlErr != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return result, urlErr
			}
			if onBrowser != nil {
				onBrowser(loginURL)
			}
		} else {
			result = event
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if scanErr != nil {
		return result, errors.New("无法读取 Codex 登录结果")
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = "Codex 操作失败，请重试"
		}
		return result, errors.New(message)
	}
	if result.Type == "" {
		return result, errors.New("Codex 操作没有返回结果")
	}
	return result, nil
}

// codexProxyLookup is replaced by tests.
var codexProxyLookup = netproxy.Proxy

// codexProxyURL resolves the proxy the user's browser would use for OpenAI
// traffic: GUI-spawned helper and core processes have no shell environment,
// so their Go HTTP clients cannot see the system proxy, and OpenAI's edge
// rejects the direct connection in proxy-required regions — the OAuth
// token exchange would fail right after the browser login. netproxy
// resolves env variables first, then the system settings; loopback stays
// direct. Empty means no proxy: keep current behaviour.
func codexProxyURL() string {
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "auth.openai.com"}}
	u, err := codexProxyLookup(req)
	if err != nil || u == nil {
		return ""
	}
	return u.String()
}

func (a *preview) refreshCodexStatus() {
	l := a.Live
	if l.Loading["codex-status"] || l.Loading["codex-login"] {
		return
	}
	l.Loading["codex-status"] = true
	epoch, ctx, dir, generation := l.epoch, l.ctx, l.Dir, l.Codex.Generation
	go func() {
		result, err := invokeCodexHelper(ctx, dir, "status", nil)
		a.post(epoch, func() {
			l.Loading["codex-status"] = false
			if generation != l.Codex.Generation {
				return
			}
			if err != nil {
				l.Error = err.Error()
				return
			}
			l.Codex.LoggedIn = result.LoggedIn
			l.Codex.Name = codexUserName(result)
		})
	}()
}

func (a *preview) startCodexLogin() {
	l := a.Live
	if l.configBusy() || l.Loading["codex-login"] {
		return
	}
	if l.Codex.LoggedIn {
		l.Error = "请先退出当前 Codex 账号，再登录其他账号"
		return
	}
	ctx, cancel := context.WithCancel(l.ctx)
	l.Codex.Cancel = cancel
	l.Codex.Generation++
	generation := l.Codex.Generation
	l.Loading["codex-login"] = true
	l.Error = ""
	l.Notice = "请在浏览器中完成 ChatGPT 登录。"
	epoch, dir := l.epoch, l.Dir
	post := func(f func()) {
		a.post(epoch, func() {
			if generation == l.Codex.Generation {
				f()
			}
		})
	}
	go func() {
		result, err := invokeCodexHelper(ctx, dir, "login", func(raw string) {
			post(func() { l.Codex.URL = raw })
			if ctx.Err() != nil {
				return
			}
			if openErr := openCodexBrowser(raw); openErr != nil {
				post(func() { l.Notice = "请点击“打开登录页面”完成 ChatGPT 授权。" })
			}
		})
		post(func() {
			l.Loading["codex-login"] = false
			l.Codex.Cancel = nil
			l.Codex.URL = ""
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					l.Error = err.Error()
				}
				return
			}
			l.Codex.LoggedIn = true
			l.Codex.Name = codexUserName(result)
			l.Notice = "已登录 ChatGPT（Codex）；在会话里用 /models 选择 Codex 模型。"
		})
	}()
}

func (a *preview) cancelCodexLogin() {
	l := a.Live
	l.Codex.Generation++
	if l.Codex.Cancel != nil {
		l.Codex.Cancel()
		l.Codex.Cancel = nil
	}
	epoch := l.epoch
	a.post(epoch, func() {
		l.Loading["codex-login"] = false
		l.Codex.URL = ""
	})
}

func (a *preview) logoutCodex() {
	l := a.Live
	if l.configBusy() || l.Loading["codex-login"] {
		return
	}
	l.Codex.Generation++
	generation := l.Codex.Generation
	l.Loading["codex-status"] = true
	epoch, ctx, dir := l.epoch, l.ctx, l.Dir
	go func() {
		_, err := invokeCodexHelper(ctx, dir, "logout", nil)
		a.post(epoch, func() {
			l.Loading["codex-status"] = false
			if generation != l.Codex.Generation {
				return
			}
			if err != nil {
				l.Error = err.Error()
				return
			}
			l.Codex.LoggedIn = false
			l.Codex.Name = ""
			l.Notice = "已退出 Codex（ChatGPT）账号。"
		})
	}()
}

func (a *preview) codexSettings(c *ui.Context) {
	l := a.Live
	ui.Column(c).Gap(10).Margin(12, 0, 0, 0).Children(func() {
		if l.Codex.LoggedIn {
			a.note(c, a.tr("已登录：")+l.Codex.Name+"（ChatGPT 账号额度）")
		} else {
			a.note(c, "尚未登录 Codex。登录后可在会话里用 /models 选择 ChatGPT 模型。")
		}
		ui.Row(c).Gap(10).Wrap().Children(func() {
			if l.Loading["codex-login"] {
				if l.Codex.URL != "" {
					raw := l.Codex.URL
					a.action(c, "打开登录页面").OnClick(func() { go openCodexBrowser(raw) })
				}
				a.action(c, "取消登录").OnClick(a.cancelCodexLogin)
			} else {
				if !l.Codex.LoggedIn {
					a.primary(c, "ChatGPT 登录 (Codex)").Disabled(l.configBusy()).OnClick(a.startCodexLogin)
				} else {
					a.action(c, "退出 Codex").Disabled(l.configBusy()).OnClick(a.logoutCodex)
				}
			}
		})
		a.note(c, "登录凭据仅保存在这台设备的用户私有目录，计入登录账号的订阅额度，无需 API key。")
	})
}
