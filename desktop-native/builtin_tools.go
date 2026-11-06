package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

var builtinToolRows = []struct{ id, label, description string }{
	{"file_read", "读取文件", "读取、查找和搜索工作区文件。"},
	{"file_write", "修改文件", "创建、修改和删除工作区文件。"},
	{"bash", "终端命令", "执行命令，命令也可能访问文件和网络。"},
	{"web_search", "联网搜索", "搜索公开网页，查询会发送到搜索服务。"},
	{"web_fetch", "读取网页", "读取公开网页的内容。"},
}

func builtinToolSettings(data any) (map[string]map[string]any, bool) {
	result := make(map[string]map[string]any)
	for _, raw := range list(object(data)["tools"]) {
		item := object(raw)
		id := str(item["id"])
		if id == "" || result[id] != nil {
			return nil, false
		}
		_, enabled := item["enabled"].(bool)
		_, editable := item["editable"].(bool)
		effective, source := str(item["effective"]), str(item["source"])
		if !enabled || !editable || (effective != "enabled" && effective != "partial" && effective != "disabled") || (source != "startup" && source != "yaml" && source != "environment" && source != "saved" && source != "command") {
			return nil, false
		}
		result[id] = item
	}
	for _, row := range builtinToolRows {
		if result[row.id] == nil {
			return nil, false
		}
	}
	return result, true
}

func (a *preview) liveBuiltinToolSettings(c *ui.Context) {
	l := a.Live
	a.heading(c, "内置工具")
	a.note(c, "设置保存到当前运行主机，新会话生效；正在运行的任务保持原有设置。")
	settings, available := builtinToolSettings(l.Data["builtin-tools"])
	busy := l.Loading["builtin-tools"] || l.Loading["builtin-tools-save"]
	if !available {
		if l.Loading["builtin-tools"] {
			a.note(c, "正在读取工具设置…")
		} else {
			a.note(c, "无法读取工具设置；请连接支持此功能的运行主机。")
			a.action(c, "重试读取工具设置").AlignSelf(ui.Start).OnClick(func() { a.resource("builtin-tools", "/settings/builtin-tools") })
		}
		return
	}
	for _, row := range builtinToolRows {
		item := settings[row.id]
		enabled := boolean(item["enabled"])
		source := map[string]string{"startup": "来源：启动配置", "yaml": "来源：YAML 配置", "environment": "来源：环境变量", "saved": "来源：主机设置", "command": "来源：启动参数"}[str(item["source"])]
		status := "已关闭"
		if enabled {
			status = "已启用"
			if str(item["effective"]) == "partial" {
				status = "部分可用：受工具过滤配置限制"
			} else if str(item["effective"]) == "disabled" {
				status = "不可用：受工具过滤配置限制"
			}
		}
		sourceText := a.tr(source)
		if key := str(item["source_detail"]); key != "" {
			sourceText += " (" + key + ")"
		}
		statusText := a.tr(status)
		var restrictions []string
		for _, raw := range list(item["restricted_by"]) {
			if label := map[string]string{"allowed_tools": "允许工具名单", "blocked_tools": "禁用工具名单"}[str(raw)]; label != "" {
				restrictions = append(restrictions, a.tr(label))
			}
		}
		if len(restrictions) > 0 {
			statusText += " (" + strings.Join(restrictions, ", ") + ")"
		}
		detail := a.tr(row.description) + " · " + sourceText + " · " + statusText
		a.preferenceRow(c, row.label, detail, func() {
			label := a.tr("内置工具") + "：" + a.tr(row.label)
			if ui.Checkbox(c.Key(row.id), &enabled, a.tr("启用")).Label(label).Disabled(busy || !boolean(item["editable"])).Changed() {
				a.saveBuiltinToolSetting(row.id, enabled)
			}
		})
	}
	a.note(c, "关闭内置工具不会限制终端命令或其他已授权工具的能力。")
	if message := l.ResourceErrors["builtin-tools-save"]; message != "" {
		a.note(c, message)
	}
}

func (a *preview) saveBuiltinToolSetting(id string, enabled bool) {
	a.operation("builtin-tools-save", "POST", "/settings/builtin-tools", map[string]any{"tool": id, "enabled": enabled}, func(data any) {
		if _, valid := builtinToolSettings(data); !valid {
			a.Live.ResourceErrors["builtin-tools-save"] = "运行主机未返回有效的工具设置，请刷新后重试。"
			return
		}
		a.Live.Versions["builtin-tools"]++
		a.Live.Data["builtin-tools"] = data
		a.Settings.message("工具设置已保存，新会话生效。")
	})
}
