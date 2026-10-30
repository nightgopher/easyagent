package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	qrcode "github.com/skip2/go-qrcode"
)

func newModelRequest(ctx context.Context, b *backendClient, key string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", b.URL+modelCatalogPath(b.URL), nil)
	if err == nil {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	return req, err
}
func (a *preview) liveMCPSettings(c *ui.Context) {
	if a.resourceFeedback(c, "mcp", a.loadSettingsPage) {
		return
	}
	s := &a.Settings
	l := a.Live
	m := object(l.Data["mcp"])
	workspace := s.Workspace
	apply := func(v any) {
		if a.Settings.Workspace == workspace {
			l.Data["mcp"] = v
		} else {
			a.loadSettingsPage()
		}
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		a.text(c, "已配置服务").FontSize(14).FontWeight(600)
		a.note(c, fmt.Sprintf("%d 个", len(list(m["servers"]))))
		ui.Spacer(c)
		a.action(c, "刷新").Disabled(l.Loading["mcp"]).OnClick(a.loadSettingsPage)
		label := "添加服务"
		if a.settingsFormDirty() {
			label = "继续编辑"
		}
		a.primary(c, label).OnClick(func() { a.beginMCPEditor(nil) })
	})
	for _, raw := range list(m["issues"]) {
		a.note(c, str(object(raw)["message"]))
	}
	if len(list(m["servers"])) == 0 {
		a.note(c, "还没有 MCP 服务，点击添加服务连接本机工具或远程服务。")
	}
	if len(list(m["servers"])) > 0 {
		ui.Column(c).Label("MCP 服务列表").Gap(0).MinWidth(0).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(8).Children(func() {
			for index, raw := range list(m["servers"]) {
				if index > 0 {
					ui.Box(c).Height(1).Background(c.Theme().Border)
				}
				server := object(raw)
				name, scope := str(server["name"]), str(server["scope"])
				params := query(map[string]string{"workspace": s.Workspace, "scope": scope})
				path := "/mcp/servers/" + pathSegment(name)
				row := ui.Column(c.Key(l.Config.ActiveHost+"\x00"+workspace+"\x00"+scope+"\x00"+name)).Label("MCP 服务："+name).Padding(12, 14).Gap(10).MinWidth(0).Disabled(l.Loading["mcp"])
				expanded := ui.Local(row, "details", func() bool { return false })
				toolsOpen := ui.Local(row, "tools", func() bool { return false })
				toolFilter := ui.Local(row, "tool-filter", func() string { return "" })
				row.Children(func() {
					ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
						scopeLabel := "个人"
						if scope == "project" {
							scopeLabel = "项目"
						}
						ui.ButtonBase(c).Label("服务详情："+name).Grow(1).MinWidth(0).AlignItems(ui.Center).Gap(10).Padding(2, 0).Radius(4).Justify(ui.Start).Expanded(*expanded).OnClick(func() { *expanded = !*expanded }).Children(func() {
							chevron := "chevron-right"
							if *expanded {
								chevron = "chevron-down"
							}
							ui.Icon(c, icons[chevron]).FontSize(12).TextColor(c.Theme().TextMuted).Shrink(0)
							ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
								ui.Text(c, name).FontSize(13.5).FontWeight(600).SingleLine()
								if description := str(server["description"]); description != "" {
									ui.Text(c, description).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine().Tooltip(description)
								}
								ui.Text(c, scopeLabel+" · "+strings.ToUpper(str(server["transport"]))+" · "+formatCount(server["tool_count"])+" 个工具").FontSize(11).TextColor(c.Theme().TextMuted).SingleLine()
							})
						})
						state := str(server["state"])
						color := c.Theme().TextMuted
						switch state {
						case "connected", "ready":
							state, color = "已连接", c.Theme().Accent
						case "connecting":
							state = "连接中"
						case "error", "failed":
							state, color = "连接失败", c.Theme().Danger
						case "disconnected":
							state = "未连接"
						case "needs_auth":
							state = "需要授权"
						case "disabled":
							state = "已停用"
						}
						if !boolean(server["enabled"]) {
							state, color = "已停用", c.Theme().TextMuted
						} else if !boolean(server["trusted"]) {
							state, color = "待信任", c.Theme().TextMuted
						}
						if state == "" {
							state = "未连接"
						}
						ui.Text(c, state).FontSize(11).TextColor(color).Shrink(0)
						enabled := boolean(server["enabled"])
						ui.Switch(c, &enabled).Label("启用 MCP 服务：" + name).Shrink(0).Disabled(l.Loading["mcp-action"]).OnChange(func() {
							action := "disable"
							if enabled {
								action = "enable"
							}
							a.operation("mcp-action", "POST", path+"/"+action+params, map[string]any{}, apply)
						})
					})
					if err := str(server["error"]); err != "" {
						ui.Text(c, err).FontSize(12).TextColor(c.Theme().Danger)
					}
					if !*expanded {
						return
					}
					ui.Row(c).Gap(8).Wrap().Children(func() {
						a.action(c, "修改配置").OnClick(func() { a.beginMCPEditor(server) })
						if boolean(server["trusted"]) {
							a.action(c, "重新连接").Disabled(l.Loading["mcp-action"] || !boolean(server["enabled"])).OnClick(func() {
								a.operation("mcp-action", "POST", path+"/reconnect"+params, map[string]any{}, apply)
							})
						} else {
							a.primary(c, "信任服务").Disabled(l.Loading["mcp-action"]).OnClick(func() { a.operation("mcp-action", "POST", path+"/trust"+params, map[string]any{}, apply) })
						}
						if str(server["state"]) == "needs_auth" {
							a.primary(c, "授权登录").Disabled(l.Loading["mcp-login"]).OnClick(func() { a.startMCPLogin(name, scope, workspace) })
						}
					})
					nativeDisclosure(c, "server-actions", "更多操作", func() {
						ui.Row(c).Gap(8).Wrap().Children(func() {
							if boolean(server["trusted"]) {
								a.action(c, "撤销信任").Disabled(l.Loading["mcp-action"]).OnClick(func() { a.operation("mcp-action", "POST", path+"/untrust"+params, map[string]any{}, apply) })
							}
							if str(server["transport"]) != "stdio" {
								if str(server["state"]) != "needs_auth" {
									a.action(c, "授权登录").Disabled(l.Loading["mcp-login"]).OnClick(func() { a.startMCPLogin(name, scope, workspace) })
								}
								a.action(c, "退出授权").Disabled(l.Loading["mcp-action"]).OnClick(func() { a.operation("mcp-action", "POST", path+"/logout"+params, map[string]any{}, apply) })
							}
							a.action(c, "删除").Disabled(l.Loading["mcp-action"]).TextColor(c.Theme().Danger).OnClick(func() {
								l.Confirm = &nativeConfirmation{"删除 MCP 服务？", name + " · " + scope + " · " + workspace, func() { a.operation("mcp-action", "DELETE", path+params, nil, apply) }}
							})
						})
					})
					toolToggle := "查看工具"
					if *toolsOpen {
						toolToggle = "收起工具"
					}
					a.action(c, toolToggle+" · "+formatCount(server["tool_count"])).Label("工具列表：" + name).AlignSelf(ui.Start).OnClick(func() { *toolsOpen = !*toolsOpen })
					if !*toolsOpen {
						return
					}
					if len(list(server["tools"])) == 0 {
						a.note(c, "暂无工具，连接服务后可刷新工具列表。")
					}
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.TextInput(c, toolFilter).Label("搜索工具："+name).Placeholder("搜索工具名称或说明").Grow(1).MinWidth(0).FontSize(12).Padding(8).Background(c.Theme().Background).Border(1, c.Theme().Border)
						a.action(c, "刷新工具").Disabled(l.Loading["mcp-action"]).OnClick(func() { a.operation("mcp-action", "POST", path+"/refresh"+params, map[string]any{}, apply) })
					})
					ui.Scroll(c).Label("服务工具：" + name).MaxHeight(280).Shrink(0).Gap(10).Children(func() {
						matches := 0
						for _, toolRaw := range list(server["tools"]) {
							tool := object(toolRaw)
							if query := strings.ToLower(strings.TrimSpace(*toolFilter)); query != "" && !strings.Contains(strings.ToLower(str(tool["name"])+" "+str(tool["description"])), query) {
								continue
							}
							matches++
							enabled := boolean(tool["enabled"])
							ui.Row(c).Gap(8).Children(func() {
								ui.Column(c).Grow(1).MinWidth(0).Children(func() { ui.Text(c, str(tool["name"])).FontSize(12); a.note(c, str(tool["description"])) })
								if ui.Checkbox(c.Key(str(tool["name"])), &enabled, "启用").Label("启用工具：" + str(tool["name"])).Disabled(l.Loading["mcp-tool"]).Changed() {
									a.operation("mcp-tool", "POST", path+"/tools/"+pathSegment(str(tool["name"]))+params, map[string]any{"enabled": enabled}, apply)
								}
							})
						}
						if matches == 0 && strings.TrimSpace(*toolFilter) != "" {
							a.note(c, "没有匹配的工具，试试其他名称或关键词。")
						}
					})
				})
			}
		})
	}
	nativeDisclosure(c, "mcp-project-settings", "项目配置", func() {
		container := ui.Column(c.Key(workspace)).Gap(8)
		draft := ui.Local(container, "workspace", func() string { return workspace })
		container.Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.End).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() { a.input(c, "项目工作区", draft, "当前主机上的绝对路径", false) })
				a.action(c, "读取").Disabled(l.Loading["mcp"]).OnClick(func() {
					next := strings.TrimSpace(*draft)
					read := func() { a.resetMCPEditor(); s.Workspace = next; l.SavedMCPForm.Workspace = next; a.loadSettingsPage() }
					if a.settingsFormDirty() {
						a.requestConfirmation("放弃尚未保存的 MCP 配置？", "切换工作区会清除尚未保存的服务配置。", read)
					} else {
						read()
					}
				})
			})
			trusted := boolean(m["project_trusted"])
			label := "信任此项目"
			if trusted {
				label = "撤销信任"
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				a.note(c, "项目服务仅在信任此项目后启用。")
				ui.Spacer(c)
				a.action(c, label).Disabled(l.Loading["mcp-trust"] || l.Loading["mcp"] || workspace == "").OnClick(func() {
					a.operation("mcp-trust", "PUT", "/mcp/project-trust", map[string]any{"workspace": workspace, "trusted": !trusted}, apply)
				})
			})
		})
	})
	if l.LoginURL != "" {
		a.note(c, "正在等待 MCP 授权回调…")
		ui.Row(c).Gap(8).Children(func() {
			a.action(c, "打开授权页面").OnClick(func() { go mygo.Shell.OpenExternal(l.LoginURL) })
			a.action(c, "取消授权").OnClick(a.cancelMCPLogin)
		})
	}
}

func (a *preview) startMCPLogin(name, scope, workspace string) {
	l := a.Live
	if l.Loading["mcp-login"] {
		return
	}
	epoch, b := l.epoch, l.Client
	a.cancelMCPLogin()
	ctx, cancel := context.WithCancel(l.hostCtx)
	l.LoginCancel = cancel
	generation := l.LoginGeneration
	if b == nil {
		return
	}
	if l.Loading["mcp-login"] {
		return
	}
	l.Loading["mcp-login"] = true
	post := func(fn func()) {
		a.post(epoch, func() {
			if generation == l.LoginGeneration {
				fn()
			}
		})
	}
	go func() {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			post(func() { l.Loading["mcp-login"] = false; l.Error = err.Error() })
			return
		}
		defer listener.Close()
		redirect := "http://" + listener.Addr().String() + "/callback"
		path := "/mcp/servers/" + pathSegment(name)
		params := query(map[string]string{"workspace": workspace, "scope": scope})
		var info map[string]any
		err = b.request(ctx, "POST", path+"/login"+params, map[string]any{"redirect_url": redirect}, &info)
		if err != nil {
			post(func() { l.Loading["mcp-login"] = false; l.Error = err.Error() })
			return
		}
		state := str(info["state"])
		authURL := str(info["authorization_url"])
		u, parseErr := url.Parse(authURL)
		if parseErr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
			post(func() { l.Loading["mcp-login"] = false; l.Error = "服务返回了无效的授权地址" })
			return
		}
		codes := make(chan string, 1)
		server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" || r.URL.Query().Get("state") != state || state == "" || r.URL.Query().Get("code") == "" {
				http.Error(w, "Invalid authorization callback", 400)
				return
			}
			select {
			case codes <- r.URL.Query().Get("code"):
				fmt.Fprint(w, "授权已收到，可以返回 EasyAgent。")
			default:
				http.Error(w, "Already received", 409)
			}
		})}
		go server.Serve(listener)
		defer server.Close()
		post(func() {
			if generation != l.LoginGeneration {
				return
			}
			l.LoginURL = authURL
			l.Notice = "请在浏览器完成 MCP 授权。"
		})
		if ctx.Err() != nil {
			return
		}
		_ = mygo.Shell.OpenExternal(authURL)
		select {
		case code := <-codes:
			err = b.request(ctx, "POST", path+"/login/complete"+params, map[string]any{"state": state, "code": code}, nil)
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
			err = errors.New("MCP 授权等待超时，请重试")
		}
		post(func() {
			if generation != l.LoginGeneration {
				return
			}
			l.Loading["mcp-login"] = false
			l.LoginURL = ""
			if err != nil {
				l.Error = err.Error()
				return
			}
			l.Notice = "MCP 授权完成。"
			a.loadSettingsPage()
		})
	}()
}
func renderMarkdown(c *ui.Context, text string) { var doc markdownDocument; doc.render(c, text) }
func (a *preview) liveFeishuSettings(c *ui.Context) {
	l := a.Live
	s := &a.Settings
	if a.resourceFeedback(c, "feishu", a.loadSettingsPage) {
		return
	}
	if l.Data["feishu"] == nil {
		a.note(c, "飞书配置暂不可用。")
		a.action(c, "重试").OnClick(a.loadSettingsPage)
		return
	}
	m := object(l.Data["feishu"])
	managed := boolean(m["managed"])
	if !managed {
		a.heading(c, "扫码绑定飞书机器人")
		a.note(c, "使用飞书扫码授权应用；完成后确认保存凭据，再启动本机桥接。")
		if len(l.QRImage) > 0 {
			ui.Image(c, l.QRBitmap).Size(200, 200)
			if !time.Now().Before(l.QRExpires) {
				ui.Text(c, "二维码已过期，请重新生成").TextColor(c.Theme().Warning).FontSize(12)
			} else {
				a.note(c, "有效期至 "+l.QRExpires.Local().Format("15:04:05"))
			}
			if l.QRUserCode != "" {
				a.note(c, l.QRUserCode)
			}
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			a.action(c, "生成授权二维码").Disabled(l.Loading["qr-begin"]).OnClick(a.beginQR)
			if l.QRURL != "" {
				a.action(c, "打开授权页面").OnClick(func() {
					if safeMarkdownLink(l.QRURL) {
						go mygo.Shell.OpenExternal(l.QRURL)
					}
				})
				a.action(c, "复制链接").OnClick(func() { c.WriteClipboard(l.QRURL); c.Toast("已复制链接") })
			}
			if l.QRCode != "" {
				a.action(c, "取消扫码").OnClick(a.cancelQR)
			}
			if l.QRAuthorized {
				a.primary(c, "确认保存授权").Disabled(l.Loading["qr-confirm"] || !time.Now().Before(l.QRExpires)).OnClick(func() {
					code, generation := l.QRCode, l.QRGeneration
					a.operation("qr-confirm", "POST", "/settings/feishu/qr/confirm", map[string]any{"device_code": code}, func(any) {
						if generation != l.QRGeneration {
							return
						}
						l.QRCode = ""
						l.QRImage = nil
						l.QRAuthorized = false
						a.loadSettingsPage()
						l.Notice = "飞书凭据已保存。"
					})
				})
			}
		})
	}
	a.heading(c, "桥接状态")
	bridge := object(m["bridge"])
	a.preferenceRow(c, "当前应用", str(m["app_id"]), func() {})
	a.preferenceRow(c, "应用密钥", map[bool]string{true: "已配置", false: "未配置"}[boolean(m["secret_configured"])], func() {})
	status := "已停止"
	if boolean(bridge["running"]) {
		status = "进程运行中"
	}
	if managed {
		status = "由运行主机管理"
	}
	a.note(c, "当前状态："+status)
	if started := str(bridge["started_at"]); started != "" {
		a.note(c, "启动时间："+started)
	}
	if boolean(m["paired"]) {
		a.note(c, "已完成用户配对")
	} else if boolean(m["pairing_unavailable"]) {
		a.note(c, "当前运行主机无法生成配对码")
	} else {
		a.note(c, "尚未完成用户配对")
	}
	for _, name := range sortedKeys(object(m["services"])) {
		service := object(object(m["services"])[name])
		a.preferenceRow(c, name, feishuServiceState(str(service["state"]))+" · "+feishuAutostart(str(service["autostart"])), func() {})
	}
	if linger := str(m["linger"]); linger != "" {
		a.note(c, "用户常驻服务："+linger)
	}
	if err := str(bridge["error"]); err != "" {
		ui.Text(c, err).TextColor(c.Theme().Danger)
	}
	if !managed {
		a.primary(c, "启动 / 重启桥接").AlignSelf(ui.Start).Disabled(l.Loading["bridge"] || !boolean(m["secret_configured"])).OnClick(func() {
			a.operation("bridge", "POST", "/settings/feishu/bridge/restart", map[string]any{}, func(v any) { l.Data["feishu"] = v })
		})
	}
	a.heading(c, "自建应用凭据"+a.dirtySuffix())
	if managed && str(m["app_id"]) != "" {
		s.AppID = str(m["app_id"])
	}
	idField := a.input(c, "App ID", &s.AppID, str(m["app_id"]), false)
	if managed && str(m["app_id"]) != "" {
		idField.Disabled(true)
	}
	a.input(c, "App Secret", &s.AppSecret, "填写后保存；留空保留服务端凭据", true)
	ui.Row(c).Gap(8).Children(func() {
		a.primary(c, "保存并重启桥接").Disabled(l.Loading["feishu-save"]).OnClick(func() {
			id := s.AppID
			if id == "" || managed && str(m["app_id"]) != "" {
				id = str(m["app_id"])
			}
			if managed {
				a.operation("feishu-save", "PUT", "/settings/feishu", map[string]any{"app_id": id, "app_secret": s.AppSecret}, func(v any) { s.AppSecret = ""; l.Data["feishu"] = v })
			} else {
				a.saveLocalFeishu(id, s.AppSecret)
			}
		})
		if a.settingsFormDirty() {
			a.action(c, "放弃修改").OnClick(a.discardSettingsForms)
		}
	})
	if managed {
		a.action(c, "获取配对码").AlignSelf(ui.Start).Disabled(l.Loading["pairing"] || boolean(m["paired"]) || boolean(m["pairing_unavailable"])).OnClick(func() { a.resource("pairing", "/settings/feishu/pairing") })
		if command := str(object(l.Data["pairing"])["command"]); command != "" {
			a.note(c, command)
			valid := validFeishuPairing(object(l.Data["pairing"]), time.Now())
			if expiry, err := time.Parse(time.RFC3339, str(object(l.Data["pairing"])["expires"])); err == nil {
				a.note(c, "有效期至 "+expiry.Local().Format("01-02 15:04:05"))
			}
			if !valid {
				ui.Text(c, "配对码已过期，请重新获取").FontSize(12).TextColor(c.Theme().Warning)
			}
			a.action(c, "复制配对命令").AlignSelf(ui.Start).Disabled(!valid).OnClick(func() {
				if validFeishuPairing(object(l.Data["pairing"]), time.Now()) {
					c.WriteClipboard(command)
				}
			})
			a.action(c, "隐藏配对码").OnClick(func() { delete(l.Data, "pairing") })
		}
	}
}
func (a *preview) beginQR() {
	l := a.Live
	a.cancelQR()
	generation := l.QRGeneration
	a.operation("qr-begin", "POST", "/settings/feishu/qr/begin", map[string]any{}, func(v any) {
		if generation != l.QRGeneration {
			return
		}
		m := object(v)
		l.QRCode = str(m["device_code"])
		l.QRURL = str(m["qr_url"])
		l.QRUserCode = str(m["user_code"])
		l.QRInterval = time.Duration(max(3, number(m["interval"]))) * time.Second
		l.QRNext = time.Now().Add(l.QRInterval)
		l.QRExpires = time.Now().Add(time.Duration(number(m["expire_in"])) * time.Second)
		data, err := qrcode.Encode(l.QRURL, qrcode.Medium, 400)
		if err != nil {
			l.Error = err.Error()
		} else {
			l.QRImage = data
			l.QRBitmap, _ = ui.DecodeBitmap(data)
		}
	})
}
func (a *preview) pollQR() {
	l := a.Live
	code, generation := l.QRCode, l.QRGeneration
	a.operation("qr-poll", "POST", "/settings/feishu/qr/poll", map[string]any{"device_code": code}, func(v any) {
		if generation == l.QRGeneration && code == l.QRCode && time.Now().Before(l.QRExpires) {
			l.QRAuthorized = boolean(object(v)["authorized"])
		}
	})
}
func (a *preview) saveLocalFeishu(id, secret string) {
	l := a.Live
	if !a.Settings.host().Local {
		l.Error = "此远程核心未开放手动凭据保存，请使用该主机提供的扫码流程"
		return
	}
	if id == "" {
		l.Error = "请输入 App ID"
		return
	}
	if strings.ContainsAny(id+secret, "\r\n") {
		l.Error = "凭据格式无效"
		return
	}
	// This is the same core credential schema, scoped by this app's EA_HOME.
	path := filepath.Join(l.Dir, "core-home", "feishu-credentials.json")
	a.run("feishu-save", func(context.Context, *backendClient) (any, error) {
		var old map[string]any
		if data, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(data, &old) != nil {
				return nil, errors.New("已有飞书凭据无法读取，未覆盖")
			}
		}
		if old == nil {
			old = map[string]any{}
		}
		if oldID := str(old["app_id"]); oldID != "" && oldID != id {
			return nil, errors.New("更换 App ID 需要重新扫码绑定；原授权已保留")
		}
		if secret == "" {
			secret = str(old["app_secret"])
		}
		if secret == "" {
			return nil, errors.New("首次配置需要 App Secret")
		}
		old["app_id"] = id
		old["app_secret"] = secret
		if str(old["platform"]) == "" {
			old["platform"] = "feishu"
		}
		return nil, atomicJSON(path, old)
	}, func(any) {
		a.Settings.AppSecret = ""
		a.Live.Notice = "飞书凭据已保存，正在重启桥接。"
		a.operation("bridge", "POST", "/settings/feishu/bridge/restart", map[string]any{}, func(v any) { l.Data["feishu"] = v })
	})
}

func modelCatalogPath(base string) string {
	if strings.HasSuffix(base, "/v1") {
		return "/models"
	}
	return "/v1/models"
}

var feishuPairingCommand = regexp.MustCompile(`^/pair [A-Za-z0-9_-]+$`)

func validFeishuPairing(pairing map[string]any, now time.Time) bool {
	expiry, err := time.Parse(time.RFC3339, str(pairing["expires"]))
	return err == nil && now.Before(expiry) && feishuPairingCommand.MatchString(str(pairing["command"]))
}
func feishuServiceState(value string) string {
	if s := map[string]string{"active": "进程运行中", "activating": "正在启动", "inactive": "已停止", "failed": "启动失败", "deactivating": "正在停止"}[value]; s != "" {
		return s
	}
	return "状态未知"
}
func feishuAutostart(value string) string {
	if s := map[string]string{"enabled": "已启用", "disabled": "未启用", "masked": "已禁用", "static": "由其他服务启动", "enabled-runtime": "仅本次运行启用"}[value]; s != "" {
		return s
	}
	return "状态未知"
}
func (a *preview) cancelQR() {
	l := a.Live
	l.QRGeneration++
	l.QRCode = ""
	l.QRImage = nil
	l.QRBitmap = nil
	l.QRURL = ""
	l.QRUserCode = ""
	l.QRAuthorized = false
}
func (a *preview) cancelMCPLogin() {
	l := a.Live
	l.LoginGeneration++
	if l.LoginCancel != nil {
		l.LoginCancel()
		l.LoginCancel = nil
	}
	l.LoginURL = ""
	l.Loading["mcp-login"] = false
}
func (a *preview) cancelSettingsTransient() {
	a.Settings.HostTokenField.CancelFocus()
	if a.Settings.Tab == "models" {
		a.clearModelCheck()
	}
	a.cancelQR()
	a.cancelMCPLogin()
	delete(a.Live.Data, "pairing")
	a.discardSettingsForms()
}

// settingsFormDirty reports whether the current page's form has edits that no
// save button has committed yet. Host and Feishu compare against the host
// entry and server state directly; model and MCP compare against snapshots
// taken at load, edit and save time.
func (a *preview) settingsFormDirty() bool {
	s, l := &a.Settings, a.Live
	switch s.Tab {
	case "models":
		want, form := l.SavedModelForm, s.modelForm
		return form.Mode != want.Mode || form.Provider != want.Provider || form.BaseURL != want.BaseURL || form.Model != want.Model ||
			form.Thinking != want.Thinking || form.ReasoningEffort != want.ReasoningEffort || form.ThinkingBudget != want.ThinkingBudget ||
			form.StreamIdleTimeout != want.StreamIdleTimeout || form.APIKey != ""
	case "connections":
		if s.HostToken != "" {
			return true
		}
		if s.EditHost == "" {
			return s.HostName != "" || s.HostURL != ""
		}
		for _, h := range l.Config.Hosts {
			if h.ID == s.EditHost && !h.Local {
				return s.HostName != h.Name || s.HostURL != h.URL
			}
		}
		return s.HostName != "" || s.HostURL != ""
	case "mcp":
		want, form := l.SavedMCPForm, s.mcpForm
		return form.Scope != want.Scope || form.MCPName != want.MCPName || form.Transport != want.Transport ||
			form.Command != want.Command || form.Args != want.Args || form.Variables != want.Variables || form.URL != want.URL ||
			form.Timeout != want.Timeout || form.JSON != want.JSON || form.Advanced != want.Advanced || form.Trust != want.Trust ||
			form.Cwd != want.Cwd || form.Description != want.Description
	case "feishu":
		m := object(l.Data["feishu"])
		if s.AppSecret != "" {
			return true
		}
		// Saving a blank ID preserves the saved value; managed hosts only
		// accept an ID while setting up an app for the first time.
		editableID := !boolean(m["managed"]) || str(m["app_id"]) == ""
		return editableID && s.AppID != "" && s.AppID != str(m["app_id"])
	}
	return false
}

// discardSettingsForms returns every settings form to its saved baseline and
// clears secret inputs, so abandoned half-filled forms never survive a page
// switch or a reopened settings window.
func (a *preview) discardSettingsForms() {
	s, l := &a.Settings, a.Live
	if s.Tab == "models" && (s.modelForm != l.SavedModelForm || s.APIKey != "") {
		a.clearModelCheck()
	}
	s.modelForm = l.SavedModelForm
	s.APIKey = ""
	s.mcpForm = l.SavedMCPForm
	s.MCPEditorOpen = false
	s.MCPEditError, s.MCPEditName, s.MCPImportName = "", "", ""
	s.HostName, s.HostURL, s.HostToken = "", "", ""
	s.RepairCredential = false
	for _, h := range l.Config.Hosts {
		if h.ID == s.EditHost && !h.Local {
			s.HostName, s.HostURL = h.Name, h.URL
			break
		}
	}
	m := object(l.Data["feishu"])
	s.AppSecret = ""
	if !boolean(m["managed"]) || str(m["app_id"]) == "" {
		s.AppID = str(m["app_id"])
	}
}
