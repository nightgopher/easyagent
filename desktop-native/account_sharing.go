package main

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"

	"github.com/egoist/mygo/ui"
)

type accountSharingStatus struct {
	Shared   bool   `json:"shared"`
	Desktop  bool   `json:"desktop"`
	Selected string `json:"selected"`
	Conflict bool   `json:"conflict"`
}

func runAccountSelection(ctx context.Context, dir string, args ...string) ([]byte, error) {
	binary, err := coreBinary()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, binary, append([]string{"accounts"}, args...)...) // #nosec G204 -- bundled core and fixed account-management arguments
	env := desktopAccountEnv(dir)
	env["EA_HOME"] = filepath.Join(dir, "core-home")
	cmd.Env = filterEnvironment(env)
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("无法读取或切换账号，请检查登录状态")
	}
	return out, nil
}
func (a *preview) refreshAccountSharing(provider string) {
	l := a.Live
	key := "account-status/" + provider
	if l.Loading[key] {
		return
	}
	l.Loading[key] = true
	epoch, ctx, dir := l.epoch, l.ctx, l.Dir
	go func() {
		data, err := runAccountSelection(ctx, dir, "status", provider, "--machine")
		var status accountSharingStatus
		if err == nil {
			err = json.Unmarshal(data, &status)
		}
		a.post(epoch, func() {
			l.Loading[key] = false
			if err != nil {
				l.Error = err.Error()
				return
			}
			l.Data[key] = status
		})
	}()
}
func (a *preview) chooseSharedAccount(provider, source string) {
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
	l.Loading["account-apply"] = true
	epoch, ctx, dir := l.epoch, l.ctx, l.Dir
	model, secrets := l.Config.Model, l.Secrets
	go func() {
		err := l.Core.ensureIdle(ctx)
		if err == nil {
			_, err = runAccountSelection(ctx, dir, "use", provider, source)
		}
		if err == nil {
			l.Core.stop()
			_, err = l.Core.start(ctx, dir, model, secrets)
		}
		a.post(epoch, func() {
			l.Loading["account-apply"] = false
			if err != nil {
				l.Error = "切换账号失败：" + err.Error()
				a.connectLive()
				return
			}
			l.Error = ""
			l.Notice = "已切换账号，原有账号均已保留。"
			a.connectLive()
			a.refreshAccountSharing(provider)
			if provider == "codex" {
				a.refreshCodexStatus()
			} else {
				a.refreshEasyCodeStatus()
			}
		})
	}()
}
func (a *preview) accountSharingChoices(c *ui.Context, provider string) {
	l := a.Live
	v, ok := l.Data["account-status/"+provider].(accountSharingStatus)
	if !ok {
		if !l.Loading["account-status/"+provider] {
			a.refreshAccountSharing(provider)
		}
		return
	}
	if v.Conflict || (v.Selected == "shared" && !v.Shared && v.Desktop) || (v.Selected == "desktop" && !v.Desktop && v.Shared) {
		if v.Conflict {
			a.note(c, "检测到桌面和命令行分别登录了账号，请选择本机桌面端使用的账号；两份登录都会保留。")
		} else {
			a.note(c, "当前选择的账号已退出，可以选择另一端仍然登录的账号。")
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			selectedButton(c, a.action(c, "使用命令行账号"), v.Selected == "shared").Disabled(l.configBusy() || !v.Shared).OnClick(func() { a.chooseSharedAccount(provider, "shared") })
			selectedButton(c, a.action(c, "使用桌面原账号"), v.Selected == "desktop").Disabled(l.configBusy() || !v.Desktop).OnClick(func() { a.chooseSharedAccount(provider, "desktop") })
		})
	}
	a.note(c, "账号可由本机桌面端和命令行复用；退出共享账号会同时影响使用该账号的另一端。")
}
