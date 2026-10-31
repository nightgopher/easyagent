package main

import (
	"net/url"

	"github.com/egoist/mygo/ui"
)

// Business conversations show their own resources; the legacy global profile
// and agent-lessons browser remain available to the original applications.
func (a *preview) businessMemoryWorkbench(c *ui.Context) {
	id := a.current().AgentID
	ui.Scroll(c).Grow(1).MinHeight(0).Padding(14).Gap(10).Children(func() {
		a.heading(c, a.businessAgentName(id)+" · 独立记忆")
		a.action(c, "管理此 Agent 的记忆").AlignSelf(ui.Start).OnClick(func() {
			a.openSettings("memory-knowledge")
			a.Settings.Business.Tab = "memories"
			a.selectMemoryAgent(id)
		})
		if a.resourceFeedback(c, "business-context-memory", a.refreshWorkbench) {
			return
		}
		items := businessDecode[[]nativeAgentMemory](object(a.Live.Data["business-context-memory"])["memories"])
		if len(items) == 0 {
			a.note(c, "尚无记忆。可以明确告诉 Agent 需要记住的事实，或打开管理页手动添加。")
		} else {
			for _, m := range items {
				ui.Column(c.Key(m.ID)).Padding(10, 0).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
					ui.Text(c, m.Key).FontSize(13).FontWeight(600)
					ui.Text(c, m.Content).FontSize(12).MaxLines(4)
					if m.Expired {
						a.note(c, "已过期，不用于后续检索")
					}
					if m.Scope == "workspace" {
						a.note(c, "工作区："+m.Workspace)
					}
				})
			}
		}
	})
}
func (a *preview) businessKnowledgeWorkbench(c *ui.Context) {
	l, w := a.Live, &a.Live.Workbench
	id := a.current().AgentID
	ui.Column(c).Padding(12).Gap(8).Children(func() {
		a.note(c, "仅检索此 Agent 已绑定的知识来源。")
		ui.Row(c).Gap(6).Children(func() {
			ui.TextInput(c, &w.KBQuery).Label("检索 Agent 知识").Placeholder("搜索知识内容…").Grow(1).MinWidth(0).OnSubmit(a.refreshWorkbench)
			a.action(c, "搜索").OnClick(a.refreshWorkbench)
		})
		a.action(c, "管理知识来源与绑定").AlignSelf(ui.Start).OnClick(func() { a.openSettings("agents") })
	})
	if a.resourceFeedback(c, "business-context-knowledge", a.refreshWorkbench) {
		return
	}
	ui.Scroll(c).Grow(1).MinHeight(0).Padding(14).Gap(12).Children(func() {
		entries := list(object(l.Data["business-context-knowledge"])["entries"])
		if len(entries) == 0 {
			a.note(c, "没有匹配知识。请检查角色绑定或更换关键词。")
		}
		for _, raw := range entries {
			h := object(raw)
			source, path := str(h["source_id"]), str(h["path"])
			ui.Column(c.Key(source+path)).Padding(10, 0).Gap(5).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
				ui.Text(c, str(h["title"])).FontSize(13).FontWeight(600)
				ui.Text(c, str(h["snippet"])).FontSize(12).MaxLines(4)
				a.note(c, str(h["source_name"])+" · "+path)
				a.action(c, "阅读原文").AlignSelf(ui.Start).OnClick(func() {
					a.operation("business-read", "GET", "/agents/"+url.PathEscape(id)+"/knowledge/"+url.PathEscape(source)+"/document"+query(map[string]string{"path": path}), nil, func(v any) {
						if a.current().AgentID == id {
							l.Data["business-context-document"] = v
						}
					})
				})
			})
		}
		if value := l.Data["business-context-document"]; value != nil {
			ui.Divider(c)
			a.action(c, "关闭原文").AlignSelf(ui.Start).OnClick(func() { delete(l.Data, "business-context-document") })
			renderMarkdown(c, str(object(value)["content"]))
		}
	})
}
