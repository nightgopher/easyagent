package main

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

type businessChoice struct{ ID, Label string }

func (a *preview) businessSelect(c *ui.Context, label string, value *string, choices []businessChoice) ui.Element {
	sel := ui.SelectBase(c, value)
	caption := "请选择"
	for _, choice := range choices {
		if choice.ID == *value {
			caption = choice.Label
		}
	}
	if caption == "请选择" && *value != "" {
		caption = "当前项不可用"
	}
	sel.Trigger.Label(label).Height(36).MinWidth(130).MaxWidth(420).Padding(6, 10).Gap(8).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(6).Children(func() {
		ui.Text(c, a.tr(caption)).FontSize(12).Grow(1).MinWidth(0).SingleLine()
		icon(c, "chevron-down")
	})
	sel.Popup(func(panel ui.Element) {
		panel.Padding(4).Background(c.Theme().SurfaceHover).Border(1, c.Theme().Border).Radius(6).MinWidth(220)
		panel.Children(func() {
			ui.Scroll(c).MaxHeight(260).Children(func() {
				for _, choice := range choices {
					sel.Item(choice.ID).Label(choice.Label).Padding(8).Justify(ui.Start).FontSize(12).Children(func() { ui.Text(c, a.tr(choice.Label)).SingleLine() })
				}
			})
		})
	})
	return sel.Trigger
}
func (a *preview) businessBadge(c *ui.Context, text string) {
	ui.Text(c, a.tr(text)).FontSize(10).Padding(2, 6).Radius(4).Background(c.Theme().SurfaceHover).TextColor(c.Theme().TextMuted).SingleLine()
}
func (a *preview) businessFooter(c *ui.Context, label string, valid bool, save func()) {
	ui.Row(c).Wrap().Gap(8).Padding(14, 0, 0, 0).Children(func() {
		a.primary(c, label).Disabled(!valid || !a.Live.Connected || a.Live.Loading["business-save"]).OnClick(save)
		a.action(c, "取消").Disabled(a.Live.Loading["business-save"]).OnClick(func() { a.businessNavigate(func() { a.Settings.Business.closeEditors() }) })
	})
	if err := a.Live.ResourceErrors["business-save"]; err != "" {
		ui.Text(c, err).FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
		a.note(c, "输入已保留。若版本冲突，请取消编辑并刷新后重新修改。")
	}
}
func (a *preview) businessSettings(c *ui.Context) {
	if a.Live == nil {
		a.note(c, "连接运行主机后，可创建拥有独立说明、工具与记忆的 Agent。")
		return
	}
	s := &a.Settings.Business
	if s.AgentEditor {
		a.businessAgentEditor(c)
		return
	}
	ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		ui.TextInput(c, &s.AgentQuery).Label("搜索业务 Agent").Placeholder("搜索名称或角色说明…").Grow(1).Basis(180).MinWidth(0)
		a.primary(c, "创建 Agent").Disabled(!a.Live.Connected).OnClick(func() { a.editBusinessAgent(nativeBusinessAgent{}) })
		a.action(c, "刷新").Disabled(a.Live.Loading["business-agents"]).OnClick(a.refreshBusiness)
	})
	if a.resourceFeedback(c, "business-agents", a.refreshBusiness) {
		return
	}
	agents := a.businessAgents()
	count := 0
	for _, v := range agents {
		if q := strings.ToLower(strings.TrimSpace(s.AgentQuery)); q != "" && !strings.Contains(strings.ToLower(v.Name+" "+v.Instructions), q) {
			continue
		}
		count++
		ui.Column(c.Key(v.ID)).Label("业务 Agent："+v.Name).Padding(16, 0).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
			ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, v.Name).FontSize(15).FontWeight(600).Grow(1).MinWidth(0)
				if v.Disabled {
					a.businessBadge(c, "已停用")
				}
				a.action(c, "编辑").Label("编辑 Agent：" + v.Name).OnClick(func() { a.editBusinessAgent(v) })
				a.primary(c, "开始会话").Label("开始会话：" + v.Name).Disabled(v.Disabled || !a.Live.Connected || a.Live.Loading["create-session"]).OnClick(func() { a.startBusinessSession(v.ID) })
			})
			ui.Text(c, v.Instructions).FontSize(12).TextColor(c.Theme().TextMuted).MaxLines(2).LineHeight(1.6)
			ui.Row(c).Wrap().Gap(8).Children(func() {
				a.businessBadge(c, fmt.Sprintf("%d 个知识来源", len(v.Knowledge)))
				a.businessBadge(c, fmt.Sprintf("%d 项工具 · %d 个技能", len(v.Tools), len(v.Skills)))
				if v.MemoryEnabled {
					a.businessBadge(c, "独立记忆已开启")
				} else {
					a.businessBadge(c, "独立记忆未开启")
				}
			})
		})
	}
	if count == 0 {
		if len(agents) == 0 {
			a.heading(c, "为长期工作创建一个 Agent")
			a.note(c, "写下它的职责，选择可使用的工具和知识来源。每次会话继续使用同一个角色与记忆空间。")
		} else {
			a.note(c, "没有匹配的 Agent。")
		}
	}
}
func (a *preview) businessAgentEditor(c *ui.Context) {
	s := &a.Settings.Business
	v := &s.Agent
	title := "创建 Agent"
	if v.ID != "" {
		title = "编辑 Agent"
	}
	a.heading(c, title)
	a.input(c, "Agent 名称", &v.Name, "例如：客户反馈助手", false)
	a.multiline(c, "角色说明", &v.Instructions, 4, 10)
	a.note(c, "说明它的职责、处理方式和边界。它会使用自己的记忆与知识授权。")
	ui.Row(c).Wrap().Gap(12).Children(func() {
		ui.Column(c).Basis(240).Grow(1).MinWidth(0).Children(func() { a.input(c, "默认模型（可选）", &v.Model, "留空使用主机默认模型", false) })
		ui.Column(c).Basis(240).Grow(1).MinWidth(0).Children(func() { a.input(c, "工作目录（可选）", &v.Workspace, "留空自动建立专属目录", false) })
	})
	a.note(c, "路径属于当前运行主机。修改工作目录后，需要重新开始会话。")
	ui.Checkbox(c, &v.MemoryEnabled, a.tr("启用独立记忆"))
	ui.Checkbox(c, &v.Disabled, a.tr("停用此 Agent"))
	a.heading(c, "知识来源")
	if a.resourceFeedback(c, "knowledge-sources", a.refreshBusiness) {
		return
	}
	sources := a.knowledgeSources()
	if len(sources) == 0 {
		a.note(c, "尚未配置来源。先在“记忆与知识”中添加目录，再回来绑定。")
	}
	known := map[string]bool{}
	for _, source := range sources {
		known[source.ID] = true
		access := "none"
		for _, b := range v.Knowledge {
			if b.SourceID == source.ID {
				access = b.Access
			}
		}
		ui.Row(c.Key(source.ID)).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Basis(180).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, source.Name).FontSize(12)
				if source.Disabled {
					a.note(c, "来源已停用，请取消绑定或先启用来源。")
				}
			})
			choices := []businessChoice{{"none", "不绑定"}, {"read", "只读检索"}}
			if source.Writable {
				choices = append(choices, businessChoice{"write", "检索与人工发布"})
			}
			if access == "write" && !source.Writable {
				choices = append(choices, businessChoice{"write", "写入权限已撤销"})
			}
			if a.businessSelect(c, "知识权限："+source.Name, &access, choices).Changed() {
				v.Knowledge = slices.DeleteFunc(v.Knowledge, func(b nativeKnowledgeBinding) bool { return b.SourceID == source.ID })
				if access != "none" {
					v.Knowledge = append(v.Knowledge, nativeKnowledgeBinding{source.ID, access})
				}
			}
		})
	}
	for _, binding := range v.Knowledge {
		if !known[binding.SourceID] {
			a.note(c, "不可用来源："+binding.SourceID)
			a.action(c, "移除不可用绑定").OnClick(func() {
				v.Knowledge = slices.DeleteFunc(v.Knowledge, func(b nativeKnowledgeBinding) bool { return b.SourceID == binding.SourceID })
			})
		}
	}
	glyph := "chevron-right"
	if s.Advanced {
		glyph = "chevron-down"
	}
	flatButton(c, glyph, a.tr("工具与技能")).Justify(ui.Start).Padding(10, 0).OnClick(func() { s.Advanced = !s.Advanced })
	if s.Advanced {
		labels := []string{"读取工作区文件", "写入工作区文件", "向用户提问", "获取当前时间", "按需发现已授权工具", "读取网页（需主机开启）", "搜索网页（需主机开启）"}
		ui.Row(c).Wrap().Gap(12).Children(func() {
			for i, name := range businessBaseTools {
				on := slices.Contains(v.Tools, name)
				if ui.Checkbox(c.Key(name), &on, a.tr(labels[i])).Changed() {
					v.Tools = slices.DeleteFunc(v.Tools, func(x string) bool { return x == name })
					if on {
						v.Tools = append(v.Tools, name)
					}
				}
			}
		})
		a.multiline(c, "额外工具名称（每行一个）", &s.ExtraTools, 2, 4)
		a.note(c, "仅填写主机已提供的扩展或 MCP 工具名称。主机权限与 MCP 授权仍会生效。")
		a.multiline(c, "绑定技能名称（每行一个）", &s.SkillNames, 2, 4)
		groups := groupLocalSkills(list(object(a.Live.Data[localSkillsKey])["skills"]))
		if len(groups) > 0 {
			a.note(c, "可从当前主机的技能中选择：")
		}
		ui.Row(c).Wrap().Gap(12).Children(func() {
			for _, g := range groups {
				name := str(g.Skill["name"])
				names := businessLines(s.SkillNames)
				on := slices.Contains(names, name)
				if ui.Checkbox(c.Key(name), &on, name).Disabled(!boolean(g.Skill["enabled"])).Changed() {
					names = slices.DeleteFunc(names, func(x string) bool { return x == name })
					if on {
						names = append(names, name)
					}
					s.SkillNames = strings.Join(names, "\n")
				}
			}
		})
		a.note(c, "绑定技能会加载摘要，并按需读取正文；技能不会扩大工具权限。")
	}
	a.businessFooter(c, "保存 Agent", strings.TrimSpace(v.Name) != "" && strings.TrimSpace(v.Instructions) != "", a.saveBusinessAgent)
}
func (a *preview) memoryKnowledgeSettings(c *ui.Context) {
	if a.Live == nil {
		a.note(c, "连接运行主机后，可配置知识来源并管理 Agent 的独立记忆。")
		return
	}
	s := &a.Settings.Business
	ui.Row(c).Gap(4).Children(func() {
		for _, tab := range []businessChoice{{"sources", "知识来源"}, {"memories", "Agent 记忆"}} {
			btn := a.action(c, tab.Label)
			if s.Tab == tab.ID || s.Tab == "" && tab.ID == "sources" {
				btn.Background(c.Theme().Accent.Alpha(.12)).TextColor(c.Theme().Accent)
			}
			btn.OnClick(func() {
				a.businessNavigate(func() {
					s.closeEditors()
					s.Tab = tab.ID
					if tab.ID == "memories" {
						if s.MemoryAgentID == "" {
							agents := a.businessAgents()
							if len(agents) > 0 {
								s.MemoryAgentID = agents[0].ID
							}
						}
						a.loadAgentMemories()
					}
				})
			})
		}
	})
	if s.Tab == "memories" {
		a.agentMemorySettings(c)
		return
	}
	if s.SourceEditor {
		a.sourceEditor(c)
		return
	}
	if s.BrowseSourceID != "" {
		a.sourceBrowser(c)
		return
	}
	ui.Row(c).Gap(8).Children(func() {
		a.note(c, "目录位于当前运行主机，Markdown 原文保留在原处。")
		ui.Spacer(c)
		a.primary(c, "添加来源").Disabled(!a.Live.Connected).OnClick(func() { a.editKnowledgeSource(nativeKnowledgeSource{}) })
		a.action(c, "刷新").OnClick(a.refreshBusiness)
	})
	if a.resourceFeedback(c, "knowledge-sources", a.refreshBusiness) {
		return
	}
	for _, v := range a.knowledgeSources() {
		ui.Column(c.Key(v.ID)).Label("知识来源："+v.Name).Padding(16, 0).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
			ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, v.Name).FontSize(15).FontWeight(600).Grow(1).MinWidth(0)
				if v.Disabled {
					a.businessBadge(c, "已停用")
				} else if v.Available {
					a.businessBadge(c, "目录可用")
				} else {
					a.businessBadge(c, "目录不可用")
				}
				a.action(c, "编辑").Label("编辑来源：" + v.Name).OnClick(func() { a.editKnowledgeSource(v) })
				a.action(c, "浏览与检索").Label("浏览来源：" + v.Name).Disabled(v.Disabled || !v.Available).OnClick(func() { a.browseKnowledge(v.ID) })
			})
			ui.Text(c, v.Path).FontSize(12).TextColor(c.Theme().TextMuted).Selectable()
			a.note(c, "纳入目录："+strings.Join(v.Directories, "、"))
			if v.Problem != "" {
				a.note(c, v.Problem)
			}
		})
	}
}
func (a *preview) sourceEditor(c *ui.Context) {
	s := &a.Settings.Business
	v := &s.Source
	a.heading(c, "配置知识来源")
	a.input(c, "来源名称", &v.Name, "例如：团队操作手册", false)
	a.input(c, "主机目录", &v.Path, "当前主机的绝对目录路径", false)
	a.multiline(c, "纳入的相对目录（每行一个）", &s.Directories, 3, 6)
	a.note(c, "填写 . 表示整个来源；例如 knowledge、docs。隐藏目录、原始对话导出与规则文件不会被索引。")
	ui.Checkbox(c, &v.Writable, a.tr("允许人工发布知识文档"))
	ui.Checkbox(c, &v.Disabled, a.tr("停用此来源"))
	a.note(c, "发布还需要 Agent 的写入绑定；保存来源不会改动原文。")
	a.businessFooter(c, "保存来源", strings.TrimSpace(v.Name) != "" && strings.TrimSpace(v.Path) != "", a.saveKnowledgeSource)
}
func (a *preview) sourceBrowser(c *ui.Context) {
	s := &a.Settings.Business
	flatButton(c, "arrow-left", "返回知识来源").AlignSelf(ui.Start).OnClick(func() { s.BrowseSourceID = ""; s.DocumentPath = "" })
	if s.DocumentPath != "" {
		a.heading(c, s.DocumentPath)
		a.action(c, "返回检索结果").AlignSelf(ui.Start).OnClick(func() { s.DocumentPath = "" })
		if !a.resourceFeedback(c, "knowledge-document", func() { a.readSourceDocument(s.DocumentPath) }) {
			renderMarkdown(c, str(object(a.Live.Data["knowledge-document"])["content"]))
		}
		return
	}
	ui.Row(c).Wrap().Gap(8).Children(func() {
		ui.TextInput(c, &s.KnowledgeQuery).Label("检索知识来源").Placeholder("搜索内容…").Grow(1).MinWidth(0).OnSubmit(a.searchBusinessKnowledge)
		a.primary(c, "搜索").OnClick(a.searchBusinessKnowledge)
		a.action(c, "重建索引").Disabled(a.Live.Loading["business-index"]).OnClick(func() {
			id := s.BrowseSourceID
			a.operation("business-index", "POST", "/knowledge/sources/"+url.PathEscape(id)+"/reindex", nil, func(any) { a.Settings.message("索引已更新。"); a.searchBusinessKnowledge() })
		})
	})
	if a.resourceFeedback(c, "knowledge-results", a.searchBusinessKnowledge) {
		return
	}
	hits := list(object(a.Live.Data["knowledge-results"])["entries"])
	if len(hits) == 0 {
		a.note(c, "没有匹配内容。请检查目录中是否有 Markdown 文档，或换一个关键词。")
	}
	for _, raw := range hits {
		h := object(raw)
		ui.Column(c.Key(str(h["path"]))).Padding(12, 0).Gap(5).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
			flatButton(c, "file", str(h["title"])).Justify(ui.Start).OnClick(func() { a.readSourceDocument(str(h["path"])) })
			ui.Text(c, str(h["snippet"])).FontSize(12).TextColor(c.Theme().TextMuted).MaxLines(3)
			a.note(c, str(h["path"]))
		})
	}
}
func (a *preview) readSourceDocument(path string) {
	s := &a.Settings.Business
	s.DocumentPath = path
	delete(a.Live.Data, "knowledge-document")
	a.resource("knowledge-document", "/knowledge/sources/"+url.PathEscape(s.BrowseSourceID)+"/document"+query(map[string]string{"path": path}))
}
func (a *preview) agentMemorySettings(c *ui.Context) {
	s := &a.Settings.Business
	if s.MemoryEditor {
		a.memoryEditor(c)
		return
	}
	if s.Publishing {
		a.memoryPublicationEditor(c)
		return
	}
	choices := []businessChoice{}
	enabled := false
	for _, v := range a.businessAgents() {
		choices = append(choices, businessChoice{v.ID, v.Name})
		if v.ID == s.MemoryAgentID {
			enabled = v.MemoryEnabled && !v.Disabled
		}
	}
	if len(choices) == 0 {
		a.note(c, "先创建一个业务 Agent，再为它保存记忆。")
		a.action(c, "管理业务 Agent").AlignSelf(ui.Start).OnClick(func() { a.openSettings("agents") })
		return
	}
	selected := s.MemoryAgentID
	ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
		if a.businessSelect(c, "选择记忆所属 Agent", &selected, choices).Changed() {
			a.selectMemoryAgent(selected)
		}
		ui.Spacer(c)
		a.primary(c, "添加记忆").Disabled(!enabled || !a.Live.Connected).OnClick(func() { a.editAgentMemory(nativeAgentMemory{}) })
		a.action(c, "刷新").Disabled(s.MemoryAgentID == "").OnClick(a.loadAgentMemories)
	})
	if s.MemoryAgentID == "" {
		a.note(c, "请选择一个 Agent 查看记忆。")
		return
	}
	if !enabled {
		a.note(c, "此 Agent 的独立记忆已关闭或角色已停用。已有记录保留供查看和删除。")
	}
	ui.Row(c).Gap(8).Children(func() {
		ui.TextInput(c, &s.MemoryQuery).Label("搜索 Agent 记忆").Placeholder("搜索记忆标题或内容…").Grow(1).MinWidth(0).OnSubmit(func() { s.AppliedMemoryQuery = s.MemoryQuery; s.MemoryOffset = 0; a.loadAgentMemories() })
		a.action(c, "搜索记忆").OnClick(func() { s.AppliedMemoryQuery = s.MemoryQuery; s.MemoryOffset = 0; a.loadAgentMemories() })
	})
	if a.resourceFeedback(c, "agent-memories", a.loadAgentMemories) {
		return
	}
	data := object(a.Live.Data["agent-memories"])
	memories := businessDecode[[]nativeAgentMemory](data["memories"])
	if len(memories) == 0 {
		a.note(c, "还没有匹配的记忆。可以手动添加，或在对话中明确告诉这个 Agent 需要记住的事实。")
	}
	for _, m := range memories {
		ui.Column(c.Key(m.ID)).Label("记忆："+m.Key).Padding(14, 0).Gap(7).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
			ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.Key).FontSize(14).FontWeight(600).Grow(1).MinWidth(0)
				scope := "此 Agent 通用"
				if m.Scope == "workspace" {
					scope = "工作区记忆"
				}
				a.businessBadge(c, scope)
				if m.Expired {
					a.businessBadge(c, "已过期")
				}
			})
			ui.Text(c, m.Content).FontSize(12).MaxLines(4).Selectable().LineHeight(1.6)
			origin := "手动保存"
			if m.SourceKind == "agent" {
				origin = "对话保存"
			}
			if m.UpdatedAt != "" {
				origin += " · " + businessTimestamp(m.UpdatedAt)
			}
			a.note(c, origin)
			if m.SourceSession != "" {
				a.note(c, "来源会话："+m.SourceSession)
			}
			if m.Workspace != "" {
				a.note(c, m.Workspace)
			}
			if m.ExpiresAt != "" {
				a.note(c, "有效期至："+businessTimestamp(m.ExpiresAt))
			}
			ui.Row(c).Wrap().Gap(8).Children(func() {
				a.action(c, "修正").Label("修正记忆：" + m.Key).Disabled(!enabled).OnClick(func() { a.editAgentMemory(m) })
				a.action(c, "整理为知识").Label("整理记忆：" + m.Key).OnClick(func() { a.prepareMemoryPublication(m) })
				a.action(c, "删除").Label("删除记忆：" + m.Key).Disabled(a.Live.Loading["business-save"]).OnClick(func() { a.deleteAgentMemory(m) })
			})
		})
	}
	ui.Row(c).Gap(8).Children(func() {
		a.action(c, "上一页").Disabled(s.MemoryOffset == 0 || a.Live.Loading["agent-memories"]).OnClick(func() { s.MemoryOffset = max(0, s.MemoryOffset-50); a.loadAgentMemories() })
		a.note(c, fmt.Sprintf("第 %d 页", s.MemoryOffset/50+1))
		a.action(c, "下一页").Disabled(!boolean(data["has_more"]) || a.Live.Loading["agent-memories"]).OnClick(func() { s.MemoryOffset += 50; a.loadAgentMemories() })
	})
}
func (a *preview) memoryEditor(c *ui.Context) {
	s := &a.Settings.Business
	m := &s.Memory
	a.heading(c, "保存记忆 · "+a.businessAgentName(s.MemoryAgentID))
	a.input(c, "记忆标题", &m.Key, "例如：周报格式", false).Disabled(m.ID != "")
	a.multiline(c, "记忆内容", &m.Content, 4, 10)
	if m.ID == "" {
		a.businessSelect(c, "记忆范围", &m.Scope, []businessChoice{{"agent", "此 Agent 通用"}, {"workspace", "仅当前工作区"}})
	} else {
		a.note(c, "范围与标题保持不变；如需更换，请另建一条记忆。")
	}
	a.input(c, "有效期（可选）", &m.ExpiresAt, "例如：2027-01-01T00:00:00Z，留空永久保存", false)
	a.businessFooter(c, "保存记忆", strings.TrimSpace(m.Key) != "" && strings.TrimSpace(m.Content) != "", a.saveAgentMemory)
}
func (a *preview) memoryPublicationEditor(c *ui.Context) {
	s := &a.Settings.Business
	a.heading(c, "整理为知识")
	a.note(c, "检查并编辑下方正文后再发布。原记忆保留，知识文档单独保存到所选来源。")
	sources := a.writableMemorySources()
	choices := []businessChoice{}
	dirs := []businessChoice{}
	for _, v := range sources {
		choices = append(choices, businessChoice{v.ID, v.Name})
		if v.ID == s.PublishSourceID {
			for _, d := range v.Directories {
				dirs = append(dirs, businessChoice{d, d})
			}
		}
	}
	if len(sources) == 0 {
		a.note(c, "此 Agent 尚未绑定可写知识来源。请先在来源中允许人工发布，再在 Agent 中选择“检索与人工发布”。")
		a.action(c, "返回记忆").AlignSelf(ui.Start).OnClick(func() { a.businessNavigate(func() { s.closeEditors() }) })
		return
	}
	if a.businessSelect(c, "发布到知识来源", &s.PublishSourceID, choices).Changed() {
		for _, v := range sources {
			if v.ID == s.PublishSourceID {
				s.PublishDirectory = v.Directories[0]
			}
		}
	}
	a.businessSelect(c, "发布目录", &s.PublishDirectory, dirs)
	a.input(c, "知识标题", &s.PublishTitle, "整理后的知识标题", false)
	a.multiline(c, "Markdown 正文", &s.PublishContent, 5, 14)
	a.heading(c, "发布预览")
	renderMarkdown(c, "# "+s.PublishTitle+"\n\n"+s.PublishContent)
	a.businessFooter(c, "确认发布", s.PublishSourceID != "" && strings.TrimSpace(s.PublishTitle) != "" && strings.TrimSpace(s.PublishContent) != "", a.publishAgentMemory)
}

func businessTimestamp(raw string) string {
	if stamp, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return stamp.Local().Format("2006-01-02 15:04")
	}
	return raw
}
