package main

import "github.com/egoist/mygo/ui"

func (a *preview) livePermissionSettings(c *ui.Context) {
	l := a.Live
	m := object(l.Data["permissions"])
	a.heading(c, "默认工具权限")
	a.note(c, "保存到当前运行主机，新会话生效。已有会话保留各自的权限选择。")
	for _, row := range []struct{ key, label, description string }{
		{"default_mode", "桌面 / 网页 / 移动端", "新会话使用此权限，也可在会话中单独切换。"},
		{"feishu_mode", "飞书", "默认完全权限；选择每次询问后，在机器人私聊卡片中批准或拒绝。"},
	} {
		if a.Settings.Tab == "feishu" && row.key != "feishu_mode" {
			continue
		}
		mode := str(m[row.key])
		available := mode == "ask" || mode == "full"
		choice := "尚未读取"
		if mode == "ask" {
			choice = "每次询问"
		}
		if mode == "full" {
			choice = "完全权限"
		}
		a.preferenceRow(c, row.label, row.description, func() {
			if a.selectControl(c.Key(row.key), "默认权限："+row.label, &choice, []string{"每次询问", "完全权限"}).Width(180).Disabled(!available || l.Loading["permissions-save"]).Changed() {
				next := "ask"
				if choice == "完全权限" {
					next = "full"
				}
				a.savePermissionDefault(row.key, next)
			}
		})
	}
	a.note(c, "完全权限自动批准普通工具操作；MCP 信任、工作区路径限制与必须回答的问题仍单独生效。")
	if str(m["default_mode"]) == "" {
		a.note(c, "无法读取默认权限，请连接支持此设置的运行主机。")
	}
	if message := l.ResourceErrors["permissions-save"]; message != "" {
		a.note(c, message)
	}
	a.action(c, "刷新默认权限").AlignSelf(ui.Start).Disabled(l.Loading["permissions-save"]).OnClick(func() { a.resource("permissions", "/settings/permissions") })
}

func (a *preview) savePermissionDefault(key, mode string) {
	a.operation("permissions-save", "POST", "/settings/permissions", map[string]string{key: mode}, func(data any) {
		m := object(data)
		for _, key := range []string{"default_mode", "feishu_mode"} {
			if value := str(m[key]); value != "ask" && value != "full" {
				a.Live.ResourceErrors["permissions-save"] = "主机返回了无效的权限配置，请刷新后重试。"
				return
			}
		}
		a.Live.Data["permissions"] = data
		a.Live.Versions["permissions"]++
		a.Settings.message("已保存默认权限，新会话生效")
	})
}
