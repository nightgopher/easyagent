package main

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/skip2/go-qrcode"
)

type nativeRemoteAccess struct {
	Generation        uint64
	Payload           string
	Bitmap            *ui.Bitmap
	Expires, NextPoll time.Time
}

func (a *preview) clearRemotePair() {
	l := a.Live
	if l == nil {
		return
	}
	r := &l.RemoteAccess
	payload := r.Payload
	r.Generation++
	r.Payload, r.Bitmap = "", nil
	a.cancelRemotePairPayload(payload)
}

func (a *preview) cancelRemotePairPayload(payload string) {
	var code map[string]any
	if json.Unmarshal([]byte(payload), &code) != nil || str(code["pair_token"]) == "" {
		return
	}
	l := a.Live
	if l.Client != nil {
		client := l.Client
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = client.request(ctx, "DELETE", "/remote-access/pair", map[string]any{"pair_token": code["pair_token"]}, nil)
		}()
	}
}

func (a *preview) beginRemotePair() {
	l := a.Live
	l.RemoteAccess.Generation++
	l.Versions["remote-access"]++
	l.Loading["remote-access"] = false
	generation := l.RemoteAccess.Generation
	l.RemoteAccess.Payload, l.RemoteAccess.Bitmap = "", nil
	a.operation("remote-pair", "POST", "/remote-access/pair", nil, func(v any) {
		if generation != l.RemoteAccess.Generation {
			data, _ := json.Marshal(v)
			a.cancelRemotePairPayload(string(data))
			return
		}
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		bitmap, err := qrcode.Encode(string(data), qrcode.Medium, 480)
		if err != nil {
			l.Error = "无法生成配对二维码"
			return
		}
		l.RemoteAccess.Bitmap, err = ui.DecodeBitmap(bitmap)
		if err != nil {
			l.Error = "无法显示配对二维码"
			return
		}
		l.RemoteAccess.Payload = string(data)
		l.RemoteAccess.Expires, _ = time.Parse(time.RFC3339, str(object(v)["expires_at"]))
	})
}

func (a *preview) liveRemoteAccessSettings(c *ui.Context) {
	s, l := &a.Settings, a.Live
	if !s.host().Local {
		a.note(c, "请在运行 EasyAgent 的那台电脑上打开此设置，生成手机配对二维码。")
		return
	}
	m := object(l.Data["remote-access"])
	if s.RelayURL == "" {
		s.RelayURL = str(m["relay_url"])
		if s.RelayURL == "" {
			s.RelayURL = "https://150.5.143.32"
		}
	}
	enabled, online := boolean(m["enabled"]), boolean(m["online"])
	busy := l.Loading["remote-save"] || l.Loading["remote-pair"] || l.Loading["remote-revoke"]
	label := "未开启"
	if enabled {
		label = "正在连接中继…"
	}
	if online {
		label = "已连接 · 可从手机访问"
	}
	a.heading(c, label)
	a.note(c, "手机与电脑之间端到端加密。中继转发加密流量；配对后手机可以查看会话和发送消息。电脑需要保持开机并运行 EasyAgent。")
	if message := str(m["error"]); message != "" {
		a.note(c, message)
	}
	if message := l.ResourceErrors["remote-access"]; message != "" {
		a.text(c, message).TextColor(c.Theme().Danger)
	}
	if !enabled {
		a.input(c, "中继地址", &s.RelayURL, "https://中继地址", false)
		a.input(c, "接入码（首次连接时填写）", &s.RelayRegistrationKey, "由中继管理员提供", true)
	}
	button := "开启远程访问"
	if enabled {
		button = "关闭远程访问"
	}
	a.action(c, button).Disabled(busy).AlignSelf(ui.Start).OnClick(func() {
		payload := map[string]any{"enabled": !enabled, "relay_url": s.RelayURL, "registration_key": s.RelayRegistrationKey}
		s.RelayRegistrationKey = ""
		a.clearRemotePair()
		l.Versions["remote-access"]++
		l.Loading["remote-access"] = false
		a.operation("remote-save", "POST", "/remote-access", payload, func(v any) { l.Data["remote-access"] = v })
	})
	if enabled {
		a.heading(c, "配对手机")
		a.action(c, "生成配对二维码").Disabled(!online || busy).AlignSelf(ui.Start).OnClick(a.beginRemotePair)
		r := &l.RemoteAccess
		if r.Bitmap != nil && time.Now().Before(r.Expires) {
			ui.Image(c, r.Bitmap).Size(240, 240).AlignSelf(ui.Start).Label("手机配对二维码")
			a.note(c, "在手机 EasyAgent 中选择“扫码配对电脑”。二维码 5 分钟内有效，只能使用一次，请勿分享给他人。")
			ui.Row(c).Gap(8).Children(func() {
				a.action(c, "复制配对码").OnClick(func() { c.WriteClipboard(r.Payload); c.Toast("已复制，请在手机配对页面粘贴") })
				a.action(c, "取消配对").OnClick(a.clearRemotePair)
			})
		} else if r.Bitmap != nil {
			a.note(c, "配对码已过期，请重新生成。")
		}
	}
	a.heading(c, "已配对设备")
	devices := list(m["devices"])
	if len(devices) == 0 {
		a.note(c, "还没有配对设备。")
	}
	for _, value := range devices {
		d := object(value)
		id, name := str(d["id"]), str(d["name"])
		ui.Row(c.Key(id)).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, name).Grow(1).MinWidth(0)
			a.action(c, "撤销").Disabled(busy).TextColor(c.Theme().Danger).OnClick(func() {
				l.Confirm = &nativeConfirmation{"撤销“" + name + "”？", "此手机将立即断开连接，再次访问需要重新扫码配对。", func() {
					l.Versions["remote-access"]++
					l.Loading["remote-access"] = false
					a.operation("remote-revoke", "DELETE", "/remote-access/devices/"+url.PathEscape(id), nil, func(v any) { l.Data["remote-access"] = v })
				}}
			})
		})
	}
}
