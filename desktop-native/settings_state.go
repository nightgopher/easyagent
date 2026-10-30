package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

type settingsPage struct {
	ID, Label, Icon, Description, Effect string
	Group                                int
}

// Shared settings UI copy definitions (preventing drift between live and demo views)
const (
	SettingCopyGlobalModelConfig = "全局模型配置"
	SettingCopyDefaultModel      = "主机默认模型"
	SettingCopyCurrentModel      = "当前会话模型"
	SettingCopyModelNote         = "新建会话使用主机默认模型；会话中单独选择的模型只影响该会话，并在重启后保留。"
	SettingCopyRemoteModelNote   = "模型配置属于服务主机。远程默认模型可通过下拉选择保存为该主机默认模型；会话模型可在对话输入框切换。"
	SettingCopyRefreshModels     = "刷新模型"
	SettingCopySaveDefaultModel  = "保存默认模型"
	SettingCopyLocalSource       = "本地模型来源"
	SettingCopyCmdlineMode       = "沿用命令行配置"
	SettingCopyDesktopMode       = "在桌面客户端配置"
	SettingCopyCmdlineNote       = "沿用 easyagent.yaml、个人配置文件和 EA_* 环境变量。"
)

// Effect badges the page with when its changes take effect: 立即生效,
// 新会话生效 or 需重启核心; empty pages are informational only.
var settingsPages = []settingsPage{
	{"general", "常规", "settings", "设置界面语言，查看版本与常用快捷键。", "", 0},
	{"appearance", "外观", "sparkle", "选择适合你的界面配色。", "立即生效", 0},
	{"usage", "用量统计", "activity", "查看近 30 天 Token 消耗与各会话用量排行。", "", 0},
	{"connections", "运行主机", "laptop", "选择任务运行的位置，管理远程 Agent 连接。", "", 1},
	{"models", "模型连接", "cpu", "配置当前主机的模型服务与默认模型。", "需重启核心", 1},
	{"mcp", "MCP 工具", "link", "为当前主机接入工具服务，管理信任与授权。", "立即生效", 1},
	{"capabilities", "技能", "code", "管理主机上的全局技能，或从市场安装新技能。", "新会话生效", 1},
	{"experiments", "电脑控制", "wrench", "启用电脑控制工具，配置审批策略与系统权限。", "新会话生效", 1},
	{"feishu", "飞书机器人", "feishu", "配置应用凭据、查看桥接状态，并完成私聊配对。", "立即生效", 1},
}

type localPreferences struct {
	Language       string            `json:"language"`
	Theme          string            `json:"theme"`
	WorkbenchWidth float32           `json:"workbench_width,omitempty"`
	TerminalHeight float32           `json:"terminal_height,omitempty"`
	Shortcuts      map[string]string `json:"shortcuts,omitempty"`
}
type demoHost struct {
	ID, Name, URL string
	Local         bool
}
type demoServer struct {
	Name, Scope, Transport, Host, Workspace      string
	Config                                       map[string]any
	Enabled, Trusted, Authorized, Expanded, Tool bool
}
type demoSkill struct {
	Name, Title, Description, Category, Content string
	Installed                                   bool
}
type feishuDemoState struct {
	AppID         string
	Saved, Bridge bool
}

// modelForm, hostForm, mcpForm and feishuForm hold one settings page's input
// state each; resetting a form resets the whole struct instead of a hand-kept
// field list. They embed into settingsState, so existing s.Field access keeps
// resolving to the owning form.
type modelForm struct {
	Mode, Provider, BaseURL, Model, APIKey                       string
	Thinking, ReasoningEffort, ThinkingBudget, StreamIdleTimeout string
	ModelCatalogSearch                                           string
	DefaultModelChoice                                           string
	Checked, HasKey                                              bool
}
type hostForm struct {
	EditHost, HostName, HostURL, HostToken string
	RepairCredential                       bool
}
type mcpForm struct {
	Workspace, Scope, MCPName, Transport, Command, Args, Variables, URL, Timeout, JSON string
	Description, Cwd                                                                   string
	Advanced, Trust, ProjectTrust, MCPExtra                                            bool
	MCPBase                                                                            map[string]any
}
type feishuForm struct {
	AppID, AppSecret                               string
	Bridge, FeishuSaved, Pairing, QR, QRAuthorized bool
}
type settingsState struct {
	modelForm
	hostForm
	mcpForm
	feishuForm
	Open                                                                     bool
	Heading                                                                  ui.Handle
	HostTokenField                                                           ui.Handle
	Tab, Search, Notice, Error                                               string
	Preferences                                                              localPreferences
	Path                                                                     string
	Host                                                                     int
	Hosts                                                                    []demoHost
	ComputerEnabled                                                          bool
	Approval                                                                 string
	Audit                                                                    []string
	HostFeishu                                                               map[string]feishuDemoState
	Servers                                                                  []*demoServer
	Skills                                                                   []*demoSkill
	SkillQuery, SkillFilter, SkillCategory, SkillDetail                      string
	ConfirmSkill, ConfirmServer, ConfirmScope, ConfirmHost, ConfirmWorkspace string
	ProjectTrustByWorkspace                                                  map[string]bool
	CapabilityTab, LocalSkillQuery                                           string
	MCPEditorOpen                                                            bool
	MCPEditName, MCPImportName, MCPEditError                                 string
	CapturingShortcutID                                                      string
	ShortcutConflict                                                         string
	Confirm                                                                  *nativeConfirmation
}

// newSettings seeds the preview/demo state. Hosts, MCP servers and skills here
// are fixture data for tests and the unconnected preview, not a mirror of any
// real deployment; live pages read from the connected host instead.
func newSettings() settingsState {
	return settingsState{
		Tab: "general", HostFeishu: map[string]feishuDemoState{}, ProjectTrustByWorkspace: map[string]bool{}, Preferences: localPreferences{Language: "中文", Theme: "dark"},
		Hosts:     []demoHost{{"local", localHostName(), "", true}, {"mini", "迷你主机", "http://192.168.5.16:8080", false}},
		hostForm:  hostForm{EditHost: "迷你主机", HostName: "迷你主机", HostURL: "http://192.168.5.16:8080"},
		modelForm: modelForm{Mode: "沿用命令行配置", Provider: "OpenAI 兼容服务 / 网关", BaseURL: "http://localhost:4001", Model: "coding"},
		Approval:  "逐批询问", mcpForm: mcpForm{Workspace: "/demo/easyagent", Scope: "个人", Transport: "stdio", Timeout: "30000"},
		Audit: []string{}, Servers: []*demoServer{{Name: "filesystem-demo", Host: "local", Scope: "个人", Transport: "stdio", Enabled: true, Trusted: true, Tool: true}},
		SkillFilter: "精选", SkillCategory: "全部", Skills: []*demoSkill{
			{"frontend-design", "界面设计", "根据现有设计系统实现界面，并验证布局与交互。", "开发", "## 界面设计\n\n这是预览内置的示例技能，不会下载或写入正式技能目录。\n\n- 对照现有界面。\n- 实现交互与响应式布局。\n- 检查可读性和键盘操作。", false},
			{"code-review", "代码审查", "检查修改中的功能、边界与回归风险。", "开发", "## 代码审查\n\n这是示例说明。\n\n阅读修改，验证边界和回归风险，报告具体问题。", true},
			{"project-notes", "项目记录", "组织项目进度、决策与验证证据。", "效率", "## 项目记录\n\n示例技能：整理项目进度与验证证据。", false},
		},
	}
}
func (a *preview) openSettings(tab string) {
	if tab == "skillMarket" {
		tab = "capabilities"
		a.Settings.CapabilityTab = "market"
	}
	if tab == "capabilities" {
		search := strings.ToLower(a.Settings.Search)
		if strings.Contains(search, "市场") || strings.Contains(search, "market") || strings.Contains(search, "安装") {
			a.Settings.CapabilityTab = "market"
		} else if a.Settings.CapabilityTab == "" {
			a.Settings.CapabilityTab = "mine"
		}
	}
	if a.Live != nil && a.Settings.Open && a.Settings.Tab != tab && a.settingsFormDirty() {
		a.Live.Confirm = &nativeConfirmation{"放弃未保存的修改？", "当前表单有未保存的修改，切换分区会丢弃这些修改。", func() {
			a.discardSettingsForms()
			a.openSettings(tab)
		}}
		return
	}
	if a.Live != nil && a.Settings.Tab != tab {
		a.cancelSettingsTransient()
	}
	a.Settings.Open = true
	a.Settings.Heading.Focus()
	a.Settings.Tab = tab
	a.Settings.Notice = ""
	a.Settings.Error = ""
	a.ModelMenu = false
	a.PermissionMenu = false
	a.MoreMenu = false
	a.HostMenu = false
	a.SlashMenu = false
	a.NewMenu = false
	if a.Live != nil {
		a.loadSettingsPage()
	}
}
func (a *preview) closeSettings() {
	if a.Live != nil {
		a.cancelSettingsTransient()
	}
	a.Settings.Open = false
	a.Settings.Heading.CancelFocus()
	a.Settings.HostTokenField.CancelFocus()
	a.Settings.AppSecret = ""
	a.Settings.APIKey = ""
	a.Settings.HostToken = ""
	a.ReturnToComposer = true
}
func (s *settingsState) page() settingsPage {
	for _, p := range settingsPages {
		if p.ID == s.Tab {
			return p
		}
	}
	if s.Tab == "skillMarket" {
		for _, p := range settingsPages {
			if p.ID == "capabilities" {
				return p
			}
		}
	}
	return settingsPages[0]
}

// settingsPageMatches reports whether a settings page matches the sidebar
// search by label, description or id; an empty query matches everything.
func settingsPageMatches(p settingsPage, search string) bool {
	search = strings.TrimSpace(strings.ToLower(search))
	if search == "" {
		return true
	}
	text := strings.ToLower(p.Label + p.Description + p.ID)
	if p.ID == "capabilities" {
		text += " 技能市场 市场 market 安装技能"
	}
	return strings.Contains(text, search)
}
func (s *settingsState) host() demoHost       { return s.Hosts[min(s.Host, len(s.Hosts)-1)] }
func (s *settingsState) message(value string) { s.Notice = value; s.Error = "" }
func validServiceURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
func (s *settingsState) saveHost() {
	if strings.TrimSpace(s.HostName) == "" || !validServiceURL(s.HostURL) {
		s.Error = "请填写名称和有效的 HTTP(S) 服务地址。"
		return
	}
	for i, h := range s.Hosts {
		if !h.Local && h.Name == s.EditHost {
			s.Hosts[i].Name = strings.TrimSpace(s.HostName)
			s.Hosts[i].URL = strings.TrimSpace(s.HostURL)
			s.selectHost(i)
			s.EditHost = s.Hosts[i].Name
			s.HostToken = ""
			s.message("演示连接配置已保存，未连接真实服务。")
			return
		}
	}
	h := demoHost{ID: "preview-" + strconv.Itoa(len(s.Hosts)), Name: strings.TrimSpace(s.HostName), URL: strings.TrimSpace(s.HostURL)}
	s.Hosts = append(s.Hosts, h)
	s.selectHost(len(s.Hosts) - 1)
	s.EditHost = h.Name
	s.HostToken = ""
	s.message("演示连接配置已保存，未连接真实服务。")
}
func (s *settingsState) checkModel() {
	s.Checked = false
	if s.Mode == "在桌面客户端配置" && (!validServiceURL(s.BaseURL) || strings.TrimSpace(s.Model) == "") {
		s.Error = "请填写有效的服务地址和默认模型。"
		return
	}
	s.Checked = true
	s.message("演示检测已完成：显示示例模型目录，没有发出网络请求。")
}
func (s *settingsState) mcpConfig() (map[string]any, error) {
	if s.Advanced {
		entry, _, err := s.selectedMCPImport()
		if err != nil {
			return nil, err
		}
		return entry.Config, validateMCPImport(entry.Config, s.MCPEditName != "")
	}
	config, err := s.mcpFormConfig()
	if err != nil {
		return nil, err
	}
	return config, validateMCPForm(config, s.MCPEditName != "")
}

func (s *settingsState) mcpFormConfig() (map[string]any, error) {
	config := map[string]any{}
	for k, v := range s.MCPBase {
		config[k] = v
	}
	for _, key := range []string{"command", "args", "url", "httpUrl", "timeout", "cwd", "description"} {
		delete(config, key)
		if value, exists := s.MCPBase[key]; exists && value == nil {
			config[key] = nil
		}
	}
	transport := s.Transport
	if transport == "" {
		transport = "http"
	}
	config["type"], config["trust"] = transport, s.Trust
	if transport == "stdio" {
		if strings.TrimSpace(s.Command) != "" {
			config["command"] = strings.TrimSpace(s.Command)
		}
		if s.Args != "" {
			config["args"] = strings.Split(s.Args, "\n")
		} else if args := list(s.MCPBase["args"]); len(args) == 1 && args[0] == "" {
			config["args"] = []string{""}
		} else if s.MCPBase["args"] != nil {
			config["args"] = []string{}
		}
		if s.Cwd != "" {
			config["cwd"] = s.Cwd
		}
	} else if s.URL != "" {
		if !validServiceURL(s.URL) {
			return nil, errors.New("请输入有效 HTTP(S) 地址")
		}
		config["url"] = s.URL
		if s.MCPEditName != "" || s.MCPBase["httpUrl"] != nil {
			// Updating the URL also replaces the alternate spelling in saved configs.
			config["httpUrl"] = nil
		}
	}
	key := "env"
	if transport != "stdio" {
		key = "headers"
	}
	vars := map[string]string{}
	for _, line := range strings.Split(s.Variables, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, errors.New("每行使用 KEY=VALUE 格式")
		}
		vars[strings.TrimSpace(k)] = v
	}
	if len(vars) > 0 || s.MCPBase[key] != nil {
		config[key] = vars
	}
	if s.Timeout != "" {
		n, err := strconv.Atoi(s.Timeout)
		if err != nil || n < 0 {
			return nil, errors.New("超时须为非负整数毫秒")
		}
		config["timeout"] = n
	}
	if s.Description != "" || s.MCPEditName != "" {
		config["description"] = s.Description
	}
	return config, nil
}
func (s *settingsState) saveMCP() {
	if strings.TrimSpace(s.MCPName) == "" {
		s.Error = "请输入服务名称"
		return
	}
	if s.Scope == "项目" && strings.TrimSpace(s.Workspace) == "" {
		s.Error = "项目配置需要工作区路径"
		return
	}
	config, err := s.mcpConfig()
	if err != nil {
		s.Error = err.Error()
		return
	}
	transport := "stdio"
	if _, ok := config["url"]; ok {
		transport = "http"
	}
	trusted, _ := config["trust"].(bool)
	for _, server := range s.Servers {
		if server.Name == strings.TrimSpace(s.MCPName) && server.Scope == s.Scope && server.Host == s.host().ID && (s.Scope != "项目" || server.Workspace == s.Workspace) {
			server.Config = config
			server.Transport = transport
			server.Trusted = trusted
			server.Enabled = true
			s.message("MCP 演示配置已保存，未启动工具服务。")
			return
		}
	}
	s.Servers = append(s.Servers, &demoServer{Name: strings.TrimSpace(s.MCPName), Host: s.host().ID, Config: config, Workspace: s.Workspace, Scope: s.Scope, Transport: transport, Enabled: true, Trusted: trusted, Tool: true})
	s.JSON = ""
	s.Variables = ""
	s.message("MCP 演示配置已保存，未启动工具服务。")
}
func (s *settingsState) loadPreferences(path string) {
	s.Path = path
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var prefs localPreferences
	if json.Unmarshal(b, &prefs) == nil && (prefs.Theme == "dark" || prefs.Theme == "light" || prefs.Theme == "system") && (prefs.Language == "中文" || prefs.Language == "English") {
		s.Preferences = prefs
		for id, keys := range prefs.Shortcuts {
			for i := range nativeShortcuts {
				if nativeShortcuts[i].ID == id && keys != "" {
					nativeShortcuts[i].Keys = keys
				}
			}
		}
	}
}
func (s *settingsState) savePreferences() bool {
	if s.Path == "" {
		return true
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		s.Error = err.Error()
		return false
	}
	b, _ := json.MarshalIndent(s.Preferences, "", "  ")
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		s.Error = err.Error()
		return false
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		s.Error = err.Error()
		return false
	}
	s.Error = ""
	return true
}
func previewTheme(base *ui.Theme, prefs localPreferences) ui.Theme {
	t := *base
	dark := prefs.Theme == "dark" || (prefs.Theme == "system" && base.Dark)
	t.Dark = dark
	t.Radius = 6
	if dark {
		t.Background = bg
		t.Surface = elevated
		t.SurfaceHover = raised
		t.SurfacePressed = raised
		t.Text = ink
		t.TextMuted = muted
		t.Border = line
		t.Accent = accent
		t.AccentText = ui.Hex("#081a17")
		t.AccentHover = ui.Hex("#6ee7b7")
	} else {
		t.Background = ui.Hex("#f8fbfa")
		t.Surface = ui.Hex("#eef5f2")
		t.SurfaceHover = ui.Hex("#e8f1ed")
		t.SurfacePressed = ui.Hex("#e1eee8")
		t.Text = ui.Hex("#16342e")
		t.TextMuted = ui.Hex("#385c50")
		t.Border = ui.Hex("#cadbd3")
		t.Accent = ui.Hex("#087f78")
		t.AccentText = ui.Hex("#ffffff")
		t.AccentHover = ui.Hex("#09685f")
	}
	t.Selection = t.Accent.Alpha(.24)
	t.Focus = t.Accent.Alpha(.4)
	return t
}

func (s *settingsState) selectHost(index int) {
	if index == s.Host {
		return
	}
	s.HostFeishu[s.host().ID] = feishuDemoState{s.AppID, s.FeishuSaved, s.Bridge}
	s.Host = index
	saved := s.HostFeishu[s.host().ID]
	s.AppID, s.FeishuSaved, s.Bridge = saved.AppID, saved.Saved, saved.Bridge
	s.AppSecret, s.APIKey, s.MCPName, s.JSON, s.Variables = "", "", "", "", ""
	s.Pairing, s.QR, s.QRAuthorized, s.ProjectTrust = false, false, false, false
	s.MCPBase = nil
	s.message("已切换演示主机，没有连接真实服务。")
}

func composerSurface(c *ui.Context) ui.Color {
	if c.Theme().Dark {
		return ui.Hex("#1a2b29")
	}
	return ui.Hex("#e7efeb")
}

func searchSurface(c *ui.Context) ui.Color {
	if c.Theme().Dark {
		return sunken
	}
	return ui.Hex("#e7efeb")
}

func (a *preview) requestConfirmation(title, detail string, action func()) {
	conf := &nativeConfirmation{Title: title, Description: detail, Action: action}
	if a.Live != nil {
		a.Live.Confirm = conf
	} else {
		a.Settings.Confirm = conf
	}
}

func (a *preview) canResetPageDefaults(pageID string) bool {
	if a.Live != nil && pageID == "mcp" {
		return false
	}
	switch pageID {
	case "general", "appearance", "models", "experiments", "mcp", "feishu":
		return true
	default:
		return false
	}
}

func (a *preview) resetPageDefaults(pageID string) {
	s := &a.Settings
	switch pageID {
	case "general":
		a.requestConfirmation(
			"恢复常规默认设置？",
			"将重置以下项：\n• 界面语言恢复为「中文」\n• 所有自定义快捷键恢复为默认绑定",
			func() {
				s.Preferences.Language = "中文"
				a.resetAllShortcuts()
				s.Preferences.Shortcuts = make(map[string]string)
				s.savePreferences()
				a.syncNativeLocale()
				s.message("常规设置已恢复默认。")
			},
		)
	case "appearance":
		a.requestConfirmation(
			"恢复外观默认设置？",
			"将重置以下项：\n• 配色主题恢复为「青夜 (dark)」",
			func() {
				s.Preferences.Theme = "dark"
				s.savePreferences()
				s.message("外观设置已恢复默认。")
			},
		)
	case "models":
		a.requestConfirmation(
			"恢复模型连接默认设置？",
			"将重置以下项：\n• 本地模型来源重置为「沿用命令行配置」\n• 清空桌面填写的模型服务地址、模型与思考配置",
			func() {
				s.Mode = "沿用命令行配置"
				s.Provider = "OpenAI 兼容服务 / 网关"
				s.BaseURL = "http://localhost:4001"
				s.Model = "coding"
				s.APIKey = ""
				s.Thinking = ""
				s.ReasoningEffort = ""
				s.ThinkingBudget = ""
				s.StreamIdleTimeout = ""
				s.DefaultModelChoice = ""
				if a.Live != nil {
					a.clearModelCheck()
				}
				s.message("模型连接设置已恢复默认。")
			},
		)
	case "experiments":
		a.requestConfirmation(
			"恢复电脑控制默认设置？",
			"将重置以下项：\n• 停用电脑控制工具\n• 审批策略恢复为「逐批询问」",
			func() {
				s.ComputerEnabled = false
				s.Approval = "逐批询问"
				if a.Live != nil {
					a.operation("computer-save", "POST", "/computer/settings", map[string]any{"enabled": false, "approval_policy": "ask"}, func(v any) {
						a.Live.Data["computer"] = v
					})
				}
				s.message("电脑控制设置已恢复默认。")
			},
		)
	case "mcp":
		a.requestConfirmation(
			"恢复 MCP 编辑表单默认设置？",
			"将重置以下项：\n• 清空当前正在编辑的 MCP 服务表单\n• 传输方式重置为 stdio、超时时间重置为 30000ms",
			func() {
				s.mcpForm = mcpForm{Workspace: s.Workspace, Scope: "个人", Transport: "stdio", Timeout: "30000"}
				s.message("MCP 编辑表单已恢复默认。")
			},
		)
	case "feishu":
		a.requestConfirmation(
			"清空飞书配置输入？",
			"将重置以下项：\n• 清空当前填写的 App ID 与 App Secret\n• 取消进行中的扫码授权",
			func() {
				s.AppID = ""
				s.AppSecret = ""
				s.QR = false
				s.QRAuthorized = false
				s.Pairing = false
				s.message("飞书配置输入已清空。")
			},
		)
	}
}
