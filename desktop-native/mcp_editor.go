package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

const mcpJSONPlaceholder = `{
  "mcpServers": {
    "my-service": {
      "url": "https://example.com/mcp"
    }
  }
}`

func (a *preview) beginMCPEditor(server map[string]any) {
	s := &a.Settings
	if server == nil && a.settingsFormDirty() {
		s.MCPEditorOpen = true
		return
	}
	open := func() {
		s.mcpForm = mcpForm{Workspace: s.Workspace, Scope: "个人", Transport: "http"}
		s.MCPEditName, s.MCPImportName, s.MCPEditError = "", "", ""
		if server != nil {
			s.MCPName, s.MCPEditName = str(server["name"]), str(server["name"])
			s.Transport, s.Trust, s.Description = str(server["transport"]), boolean(server["trusted"]), str(server["description"])
			s.AlwaysLoad = boolean(server["always_load"])
			if str(server["scope"]) == "project" {
				s.Scope = "项目"
			}
		}
		a.Live.SavedMCPForm = s.mcpForm
		delete(a.Live.ResourceErrors, "mcp-save")
		s.MCPEditorOpen = true
	}
	if a.settingsFormDirty() {
		a.requestConfirmation("放弃尚未保存的 MCP 配置？", "打开另一个服务前，当前未保存的编辑会被清除。", open)
		return
	}
	open()
}

func (a *preview) resetMCPEditor() {
	s := &a.Settings
	s.mcpForm = mcpForm{Workspace: s.Workspace}
	a.Live.SavedMCPForm = s.mcpForm
	s.MCPEditName, s.MCPImportName, s.MCPEditError = "", "", ""
	s.MCPEditorOpen = false
	delete(a.Live.ResourceErrors, "mcp-save")
}

func (s *settingsState) mcpSubmission() (mcpImportEntry, int, error) {
	entry, count := mcpImportEntry{Name: strings.TrimSpace(s.MCPName)}, 1
	var err error
	if s.Advanced {
		entry, count, err = s.selectedMCPImport()
		if err == nil {
			err = validateMCPImport(entry.Config, s.MCPEditName != "")
		}
	} else {
		entry.Config, err = s.mcpConfig()
	}
	if err != nil {
		return entry, count, err
	}
	if !mcpImportNamePattern.MatchString(entry.Name) {
		return entry, count, errors.New("服务名称须为 1–64 位字母、数字、下划线或短横线")
	}
	if s.MCPEditName != "" && entry.Name != s.MCPEditName {
		return entry, count, errors.New("修改配置时请保留原服务名称；新服务请通过「添加服务」创建。")
	}
	if s.Scope == "项目" && strings.TrimSpace(s.Workspace) == "" {
		return entry, count, errors.New("项目配置需要工作区路径")
	}
	return entry, count, nil
}

func (a *preview) saveMCPEditor() {
	s, l := &a.Settings, a.Live
	entry, _, err := s.mcpSubmission()
	if err != nil {
		s.MCPEditError = err.Error()
		return
	}
	scope, workspace := "user", s.Workspace
	if s.Scope == "项目" {
		scope = "project"
	}
	path := "/mcp/servers/" + pathSegment(entry.Name) + query(map[string]string{"workspace": workspace, "scope": scope})
	a.operation("mcp-save", "PUT", path, entry.Config, func(v any) {
		if s.Workspace == workspace {
			l.Data["mcp"] = v
		} else {
			a.loadSettingsPage()
		}
		a.resetMCPEditor()
		l.Notice = "MCP 配置已保存。"
	})
}

func (a *preview) mcpEditor(c *ui.Context) {
	s, l := &a.Settings, a.Live
	if !s.Open || s.Tab != "mcp" || !s.MCPEditorOpen {
		return
	}
	width, height := c.Size()
	busy := l.Loading["mcp-save"]
	open := s.MCPEditorOpen
	ui.DialogBase(c.Key("mcp-editor"), &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, .45))
		panel.Label("MCP 配置弹窗").Width(min(640, width-40)).MaxHeight(height-40).Padding(24).Gap(18).Radius(12).Background(c.Theme().Background).Border(1, c.Theme().Border)
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			title := "添加 MCP 服务"
			if s.MCPEditName != "" {
				title = "修改 MCP 配置"
			}
			a.text(c, title).FontSize(20).FontWeight(600).Role(ui.RoleHeading).Grow(1)
			iconButton(c, "x", "关闭 MCP 配置").Disabled(busy).OnClick(func() { s.MCPEditorOpen = false })
		})
		ui.Row(c).AlignSelf(ui.Start).Padding(3).Gap(2).Radius(8).Background(c.Theme().Surface).Border(1, c.Theme().Border).Disabled(busy).Children(func() {
			for _, tab := range []struct {
				label string
				json  bool
			}{{"表单配置", false}, {"JSON 导入", true}} {
				selected := s.Advanced == tab.json
				button := flatButton(c, "", tab.label).Padding(8, 18).Selected(selected).OnClick(func() {
					if s.Advanced != tab.json {
						s.toggleAdvanced()
						s.MCPEditError, s.Error = s.Error, ""
					}
				})
				if selected {
					button.TextColor(c.Theme().Text).Background(c.Theme().SurfaceHover)
				}
			}
		})
		ui.Scroll(c).Label("MCP 配置内容").MinHeight(0).MaxHeight(max(100, height-280)).Shrink(1).Gap(16).Disabled(busy).Children(func() {
			if s.Advanced {
				a.mcpJSONEditor(c)
			} else {
				a.mcpFormEditor(c)
			}
		})
		if message := s.MCPEditError; message != "" {
			ui.Text(c, message).FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
		}
		if message := l.ResourceErrors["mcp-save"]; message != "" {
			ui.Text(c, message).FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
		}
		_, count, err := s.mcpSubmission()
		if !s.Advanced && err != nil && s.MCPName != "" && (s.Command != "" || s.URL != "" || s.MCPEditName != "") {
			ui.Text(c, err.Error()).FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
		}
		projectBlocked := s.Scope == "项目" && !boolean(object(l.Data["mcp"])["project_trusted"])
		if projectBlocked {
			a.note(c, "请先在「项目配置」中信任当前项目，再保存项目服务。")
		}
		ui.Row(c).Gap(10).Wrap().AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Padding(16, 0, 0, 0).Children(func() {
			a.text(c, "保存到").FontSize(12).TextColor(c.Theme().TextMuted)
			a.selectControl(c, "配置范围", &s.Scope, []string{"个人", "项目"}).Width(100).Disabled(busy || s.MCPEditName != "")
			ui.Spacer(c)
			a.action(c, "取消").Disabled(busy).OnClick(a.resetMCPEditor)
			label := "保存配置"
			if count > 1 {
				label = "保存所选服务"
			}
			if busy {
				label = "正在保存…"
			}
			a.primary(c, label).Disabled(busy || err != nil || projectBlocked).OnClick(a.saveMCPEditor)
		})
	})
	if !open && !busy {
		// Escape and the close button retain the draft; Cancel explicitly discards it.
		s.MCPEditorOpen = false
	}
}

func (a *preview) mcpFormEditor(c *ui.Context) {
	s := &a.Settings
	editing := s.MCPEditName != ""
	ui.Row(c).Gap(16).Wrap().Children(func() {
		ui.Column(c).Basis(220).Grow(1).MinWidth(0).Children(func() { a.input(c, "服务名称", &s.MCPName, "例如 filesystem", false).Disabled(editing) })
		ui.Column(c).Basis(180).Grow(1).MinWidth(0).Children(func() {
			a.dropdown(c, "服务协议", &s.Transport, []string{"http", "stdio", "sse"}).Disabled(editing)
		})
	})
	if editing {
		a.note(c, "留空的连接参数和凭据保留原值；JSON 中可用 null 显式清除。")
	}
	if s.Transport == "stdio" {
		a.input(c, "启动命令", &s.Command, "例如 npx、uvx 或可执行文件路径", false)
		a.multiline(c, "命令参数 · 每行一个", &s.Args, 2, 4)
	} else {
		a.input(c, "服务 URL", &s.URL, "https://example.com/mcp", false)
	}
	nativeDisclosure(c, "mcp-editor-options", "更多选项", func() {
		a.input(c, "备注（可选）", &s.Description, "说明这个服务的用途", false)
		if s.Transport == "stdio" {
			a.input(c, "工作目录（可选）", &s.Cwd, "当前主机上的绝对路径", false)
		}
		label := "请求头 · KEY=VALUE"
		if s.Transport == "stdio" {
			label = "环境变量 · KEY=VALUE"
		}
		a.multiline(c, label, &s.Variables, 2, 4)
		a.input(c, "超时（毫秒）", &s.Timeout, "沿用默认超时", false)
		ui.Checkbox(c, &s.AlwaysLoad, a.tr("始终加载此服务的工具"))
		a.note(c, "仅影响工具定义的加载方式；执行仍遵循原有授权。")
	})
	ui.Checkbox(c, &s.Trust, "信任并连接此服务")
}

func (a *preview) mcpJSONEditor(c *ui.Context) {
	s := &a.Settings
	a.note(c, "粘贴 MCP JSON，自动识别服务名称与连接参数。支持 mcpServers 或单个服务对象。")
	ui.TextArea(c, &s.JSON).Label("标准 MCP 配置").Placeholder(mcpJSONPlaceholder).Lines(9, 12).Font("monospace").FontSize(12).Padding(12).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(6).OnChange(func() { s.MCPEditError = "" })
	if strings.TrimSpace(s.JSON) == "" {
		return
	}
	entries, wrapped, err := parseMCPImport(s.JSON)
	if err != nil {
		ui.Text(c, err.Error()).FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
		return
	}
	if !wrapped {
		a.input(c, "服务名称", &s.MCPName, "为这个服务取一个名称", false).Disabled(s.MCPEditName != "")
	} else if len(entries) > 1 {
		_, _, _ = s.selectedMCPImport()
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name
		}
		a.dropdown(c, "选择要导入的服务", &s.MCPImportName, names).Label("导入服务选择")
		a.note(c, fmt.Sprintf("识别到 %d 个服务，本次只保存所选服务。", len(entries)))
	}
	entry, _, err := s.mcpSubmission()
	if err != nil {
		ui.Text(c, err.Error()).FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
		return
	}
	protocol := "HTTP"
	if s.MCPEditName != "" {
		protocol = strings.ToUpper(s.Transport)
	}
	if str(entry.Config["command"]) != "" {
		protocol = "STDIO"
	} else if str(entry.Config["type"]) == "sse" {
		protocol = "SSE"
	} else if str(entry.Config["url"]) != "" || str(entry.Config["httpUrl"]) != "" {
		protocol = "HTTP"
	}
	a.text(c, "已识别 · "+entry.Name+" · "+protocol).FontSize(12).TextColor(c.Theme().Accent).Role(ui.RoleStatus)
	if boolean(entry.Config["trust"]) {
		a.note(c, "配置包含 trust: true，保存后将连接此服务。")
	} else if s.MCPEditName == "" {
		a.note(c, "保存后可在服务详情中信任并连接。")
	}
}

// Normalize form values through JSON before checking the same import contract.
func validateMCPForm(config map[string]any, partial bool) error {
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	var normalized map[string]any
	if err = json.Unmarshal(data, &normalized); err != nil {
		return err
	}
	return validateMCPImport(normalized, partial)
}
