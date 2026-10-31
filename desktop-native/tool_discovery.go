package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
)

func toolDiscoveryChoice(data any) (string, bool) {
	switch str(object(data)["mode"]) {
	case "off":
		return "完整加载", true
	case "local":
		return "按需加载", true
	default:
		return "尚未读取", false
	}
}

func (a *preview) toolDiscoverySettings(c *ui.Context) {
	l := a.Live
	choice, available := toolDiscoveryChoice(l.Data["tool-discovery"])
	busy := l.Loading["tool-discovery"] || l.Loading["tool-discovery-save"]
	a.preferenceRow(c, "工具加载", "保留基础工具，其他工具在需要时搜索并加载。", func() {
		if a.selectControl(c.Key("tool-loading"), "工具加载模式", &choice, []string{"完整加载", "按需加载"}).Width(160).Disabled(busy || !available).Changed() {
			mode := "off"
			if choice == "按需加载" {
				mode = "local"
			}
			a.saveToolDiscovery(mode)
		}
	})
	if !available && !busy {
		a.note(c, "无法读取工具加载设置；旧版运行主机需要更新后使用。")
		a.action(c, "重试读取工具加载设置").AlignSelf(ui.Start).OnClick(func() { a.resource("tool-discovery", "/settings/tool-discovery") })
	} else {
		a.note(c, "应用于当前运行主机 · 下一次任务生效")
	}
	a.resourceFeedback(c, "tool-discovery-save")
}

func (a *preview) saveToolDiscovery(mode string) {
	a.operation("tool-discovery-save", "POST", "/settings/tool-discovery", map[string]any{"mode": mode}, func(data any) {
		if _, valid := toolDiscoveryChoice(data); !valid {
			a.Live.ResourceErrors["tool-discovery-save"] = "运行主机未返回有效的工具加载设置，请更新后重试。"
			return
		}
		a.Live.Versions["tool-discovery"]++
		a.Live.Data["tool-discovery"] = data
		a.Settings.message("工具加载已保存，下一次任务生效。")
	})
}

func (a *preview) toolDiscoveryUsageText(usage map[string]any) string {
	if str(usage["mode"]) != "local" {
		return ""
	}
	return fmt.Sprintf(a.tr("常驻 %s · 已按需加载 %s · 可搜索 %s"), formatCount(usage["resident"]), formatCount(usage["loaded"]), formatCount(usage["searchable"]))
}
