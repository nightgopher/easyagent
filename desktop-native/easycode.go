package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type nativeEasyCodeModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type nativeEasyCodeEvent struct {
	Type     string `json:"type"`
	URL      string `json:"url"`
	LoggedIn bool   `json:"logged_in"`
	User     struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"user"`
	Models     []nativeEasyCodeModel `json:"models"`
	ModelError string                `json:"model_error"`
}
type nativeEasyCodeState struct {
	LoggedIn    bool
	NeedsRepair bool
	Name, URL   string
	Models      []nativeEasyCodeModel
	Cancel      context.CancelFunc
	Generation  uint64
}

var openEasyCodeBrowser = func(raw string) error { return mygo.Shell.OpenExternal(raw) }
var invokeEasyCodeHelper = runEasyCodeHelper

func (a *preview) easyCodeSettings(c *ui.Context) {
	l, s := a.Live, &a.Settings
	ui.Column(c).Gap(10).Margin(12, 0, 0, 0).Children(func() {
		if l.EasyCode.LoggedIn {
			a.note(c, a.tr("已登录：")+l.EasyCode.Name)
		} else {
			a.note(c, "尚未登录 EasyCode。")
		}
		ui.Row(c).Gap(10).Wrap().Children(func() {
			if l.Loading["easycode-login"] {
				if l.EasyCode.URL != "" {
					raw := l.EasyCode.URL
					a.action(c, "打开登录页面").OnClick(func() { go openEasyCodeBrowser(raw) })
				}
				a.action(c, "取消登录").OnClick(a.cancelEasyCodeLogin)
			} else {
				if !l.EasyCode.LoggedIn {
					a.primary(c, "飞书登录").Disabled(l.configBusy()).OnClick(a.startEasyCodeLogin)
					if l.EasyCode.NeedsRepair || l.Config.Model.Mode == "easycode" {
						a.action(c, "清除 EasyCode 登录信息").Disabled(l.configBusy()).OnClick(a.logoutEasyCode)
					}
				} else {
					a.action(c, "退出 EasyCode").Disabled(l.configBusy()).OnClick(a.logoutEasyCode)
				}
			}
		})
		if l.EasyCode.LoggedIn {
			choices := make([]string, 0, len(l.EasyCode.Models))
			for _, m := range l.EasyCode.Models {
				choices = append(choices, m.ID)
			}
			if len(choices) > 0 {
				if s.Model == "" {
					s.Model = preferredEasyCodeModel(l.EasyCode.Models)
				}
				a.selectControl(c.Key("easycode-model"), "EasyCode 默认模型", &s.Model, choices).Width(300).MaxWidth(420).Disabled(l.configBusy() || l.Loading["easycode-models"])
			} else {
				a.note(c, "请刷新账号可用的模型目录。")
			}
		}
		a.note(c, "登录凭据仅保存在这台设备的用户私有目录；退出不会影响 EasyCode 其他客户端。")
	})
}

// The core owns the callback and private credential file. Only the temporary
// loopback URL and non-secret account/model metadata travel over this pipe.
func runEasyCodeHelper(ctx context.Context, dir, action string, onBrowser func(string)) (nativeEasyCodeEvent, error) {
	var result nativeEasyCodeEvent
	binary, err := coreBinary()
	if err != nil {
		return result, err
	}
	cmd := exec.CommandContext(ctx, binary, "easycode", action, "--machine")
	cmd.Env = filterEnvironment(map[string]string{"EA_HOME": filepath.Join(dir, "core-home")})
	out, err := cmd.StdoutPipe()
	if err != nil {
		return result, errors.New("无法启动 EasyCode 登录")
	}
	stderr := newRingBuffer(4096)
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		return result, errors.New("无法启动 EasyCode 登录")
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var event nativeEasyCodeEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Type == "browser" {
			u, err := url.Parse(event.URL)
			if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" || u.Path != "/" || u.Query().Get("state") == "" || net.ParseIP(u.Hostname()) == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return result, errors.New("登录程序返回了无效的本机地址")
			}
			if onBrowser != nil {
				onBrowser(event.URL)
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
		return result, errors.New("无法读取 EasyCode 登录结果")
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = "EasyCode 操作失败，请重试"
		}
		return result, errors.New(message)
	}
	if result.Type == "" {
		return result, errors.New("EasyCode 操作没有返回结果")
	}
	return result, nil
}

func (a *preview) refreshEasyCodeStatus() {
	l := a.Live
	if l.Loading["easycode-status"] || l.Loading["easycode-login"] {
		return
	}
	l.Loading["easycode-status"] = true
	epoch, ctx, dir, generation := l.epoch, l.ctx, l.Dir, l.EasyCode.Generation
	go func() {
		result, err := invokeEasyCodeHelper(ctx, dir, "status", nil)
		a.post(epoch, func() {
			l.Loading["easycode-status"] = false
			if generation != l.EasyCode.Generation {
				return
			}
			if err != nil {
				l.EasyCode.NeedsRepair = true
				l.Error = err.Error()
				return
			}
			l.EasyCode.NeedsRepair = false
			l.EasyCode.LoggedIn = result.LoggedIn
			l.EasyCode.Name = easyCodeUserName(result)
			if result.LoggedIn && a.Settings.Mode == SettingCopyEasyCodeMode && len(l.EasyCode.Models) == 0 {
				a.refreshEasyCodeModels()
			}
		})
	}()
}

func easyCodeUserName(event nativeEasyCodeEvent) string {
	if event.User.Name != "" {
		return event.User.Name
	}
	if event.User.Email != "" {
		return event.User.Email
	}
	return "当前账号"
}

func (a *preview) startEasyCodeLogin() {
	l := a.Live
	if l.configBusy() || l.Loading["easycode-login"] {
		return
	}
	if l.EasyCode.LoggedIn {
		l.Error = "请先退出当前 EasyCode 账号，再登录其他账号"
		return
	}
	for _, s := range a.Sessions {
		if s.Running || s.Pending || s.Sending {
			l.Error = "请先等待或停止正在运行的任务，再切换账号"
			return
		}
	}
	ctx, cancel := context.WithCancel(l.ctx)
	l.EasyCode.Cancel = cancel
	l.EasyCode.Generation++
	generation := l.EasyCode.Generation
	l.Loading["easycode-login"] = true
	l.Error = ""
	l.Notice = "请在浏览器中选择组织并完成飞书登录。"
	epoch, dir := l.epoch, l.Dir
	post := func(f func()) {
		a.post(epoch, func() {
			if generation == l.EasyCode.Generation {
				f()
			}
		})
	}
	go func() {
		if err := l.Core.ensureIdle(ctx); err != nil {
			post(func() { l.Loading["easycode-login"] = false; l.EasyCode.Cancel = nil; l.Error = err.Error() })
			return
		}
		status, err := invokeEasyCodeHelper(ctx, dir, "status", nil)
		if err != nil || status.LoggedIn {
			post(func() {
				l.Loading["easycode-login"] = false
				l.EasyCode.Cancel = nil
				if err != nil {
					l.EasyCode.NeedsRepair = true
					l.Error = err.Error()
				} else {
					l.EasyCode.LoggedIn = true
					l.EasyCode.Name = easyCodeUserName(status)
					l.Error = "请先退出当前 EasyCode 账号，再登录其他账号"
				}
			})
			return
		}
		result, err := invokeEasyCodeHelper(ctx, dir, "login", func(raw string) {
			post(func() { l.EasyCode.URL = raw })
			if ctx.Err() != nil {
				return
			}
			if openErr := openEasyCodeBrowser(raw); openErr != nil {
				post(func() { l.Notice = "请点击“打开登录页面”完成飞书授权。" })
			}
		})
		post(func() {
			l.Loading["easycode-login"] = false
			l.EasyCode.Cancel = nil
			l.EasyCode.URL = ""
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					l.Error = err.Error()
				}
				return
			}
			l.EasyCode.LoggedIn = true
			l.EasyCode.NeedsRepair = false
			l.EasyCode.Name = easyCodeUserName(result)
			l.EasyCode.Models = result.Models
			if result.ModelError != "" {
				l.Error = "登录成功，但获取模型失败：" + result.ModelError
				return
			}
			model := preferredEasyCodeModel(result.Models)
			if model == "" {
				l.Error = "登录成功，但当前账号没有可用模型"
				return
			}
			a.Settings.Mode = SettingCopyEasyCodeMode
			a.Settings.Model = model
			a.applyEasyCodeModel(model)
		})
	}()
}

func preferredEasyCodeModel(models []nativeEasyCodeModel) string {
	for _, m := range models {
		if m.ID == "deepseek-flash" {
			return m.ID
		}
	}
	for _, m := range models {
		if m.ID != "" {
			return m.ID
		}
	}
	return ""
}

func (a *preview) cancelEasyCodeLogin() {
	l := a.Live
	l.EasyCode.Generation++
	if l.EasyCode.Cancel != nil {
		l.EasyCode.Cancel()
		l.EasyCode.Cancel = nil
	}
	l.EasyCode.URL = ""
	l.Loading["easycode-login"] = false
	l.Notice = "已取消飞书登录。"
}

func (a *preview) refreshEasyCodeModels() {
	l := a.Live
	if l.Loading["easycode-models"] || l.Loading["easycode-login"] {
		return
	}
	l.Loading["easycode-models"] = true
	epoch, ctx, dir, generation := l.epoch, l.ctx, l.Dir, l.EasyCode.Generation
	go func() {
		result, err := invokeEasyCodeHelper(ctx, dir, "models", nil)
		a.post(epoch, func() {
			l.Loading["easycode-models"] = false
			if generation != l.EasyCode.Generation {
				return
			}
			if err != nil {
				l.Error = err.Error()
				return
			}
			l.EasyCode.LoggedIn = true
			l.EasyCode.Models = result.Models
			found := false
			for _, m := range result.Models {
				if m.ID == a.Settings.Model {
					found = true
					break
				}
			}
			if !found {
				a.Settings.Model = preferredEasyCodeModel(result.Models)
			}
			l.Error = ""
			l.Notice = "已读取账号可用模型；未发送推理请求。"
		})
	}()
}

func (a *preview) applyEasyCodeModel(model string) {
	l := a.Live
	if l.configBusy() {
		return
	}
	if !l.EasyCode.LoggedIn {
		l.Error = "请先使用飞书登录 EasyCode"
		return
	}
	valid := false
	for _, item := range l.EasyCode.Models {
		if item.ID == model {
			valid = true
			break
		}
	}
	if !valid {
		l.Error = "请选择该账号可用的模型，或刷新模型目录"
		return
	}
	for _, s := range a.Sessions {
		if s.Running || s.Pending || s.Sending {
			l.Error = "请先等待或停止正在运行的任务"
			return
		}
	}
	next := cloneConfig(l.Config)
	old := cloneConfig(l.Config)
	if old.Model.Mode != "easycode" {
		previous := old.Model
		next.PreviousModel = &previous
	}
	next.Model = modelConfig{Mode: "easycode", Provider: "deepv", Model: model}
	l.Loading["easycode-apply"] = true
	epoch, ctx, dir, secrets := l.epoch, l.ctx, l.Dir, l.Secrets
	go func() {
		err := l.Core.ensureIdle(ctx)
		changed, stopped := false, false
		if err == nil {
			err = atomicJSON(filepath.Join(dir, "connections.json"), next)
			changed = err == nil
		}
		if err == nil {
			l.Core.stop()
			stopped = true
			var client *backendClient
			client, err = l.Core.start(ctx, dir, next.Model, secrets)
			if err == nil {
				err = client.request(ctx, "PUT", "/models/default", map[string]any{"provider": "deepv", "model": model}, nil)
			}
		}
		if err != nil && changed {
			_ = atomicJSON(filepath.Join(dir, "connections.json"), old)
			if stopped {
				l.Core.stop()
				_, _ = l.Core.start(ctx, dir, old.Model, secrets)
			}
		}
		a.post(epoch, func() {
			l.Loading["easycode-apply"] = false
			if err != nil {
				if changed {
					a.connectLive()
				}
				l.Error = "启用 EasyCode 失败：" + err.Error()
				return
			}
			l.Config = next
			a.syncSettingsConfig()
			l.Notice = "已启用 EasyCode，使用当前飞书账号的模型额度。"
			a.connectLive()
		})
	}()
}

func (a *preview) logoutEasyCode() {
	l := a.Live
	if l.configBusy() {
		return
	}
	for _, s := range a.Sessions {
		if s.Running || s.Pending || s.Sending {
			l.Error = "请先等待或停止正在运行的任务"
			return
		}
	}
	old := cloneConfig(l.Config)
	next := cloneConfig(old)
	l.EasyCode.Generation++
	generation := l.EasyCode.Generation
	if old.Model.Mode == "easycode" {
		next.Model = modelConfig{Mode: "inherit"}
		if old.PreviousModel != nil {
			next.Model = *old.PreviousModel
		}
		next.PreviousModel = nil
	}
	l.Loading["easycode-logout"] = true
	epoch, ctx, dir, secrets := l.epoch, l.ctx, l.Dir, l.Secrets
	go func() {
		err := l.Core.ensureIdle(ctx)
		removed, saved := false, false
		if err == nil {
			_, err = invokeEasyCodeHelper(ctx, dir, "logout", nil)
			removed = err == nil
		}
		if err == nil && old.Model.Mode == "easycode" {
			err = atomicJSON(filepath.Join(dir, "connections.json"), next)
			saved = err == nil
			if err == nil {
				l.Core.stop()
				_, err = l.Core.start(ctx, dir, next.Model, secrets)
			}
		}
		a.post(epoch, func() {
			l.Loading["easycode-logout"] = false
			if removed {
				l.EasyCode = nativeEasyCodeState{Generation: generation}
			}
			if saved {
				l.Config = next
				a.syncSettingsConfig()
			}
			if err != nil {
				if saved {
					a.connectLive()
				}
				l.Error = fmt.Sprintf("退出或恢复原模型时失败：%v", err)
				return
			}
			l.EasyCode = nativeEasyCodeState{Generation: generation}
			l.Config = next
			a.syncSettingsConfig()
			l.Notice = "已退出 EasyAgent 中的 EasyCode 账号。"
			if old.Model.Mode == "easycode" {
				a.connectLive()
			}
		})
	}()
}
