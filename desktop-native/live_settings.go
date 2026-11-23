package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (a *preview) connectionLabel() string {
	l := a.Live
	if l.Connecting {
		return "正在连接…"
	}
	if l.Connected {
		return "已连接"
	}
	return "未连接"
}
func (a *preview) operation(key, method, path string, body any, done func(any)) {
	l := a.Live
	if l.Loading[key] {
		return
	}
	l.Loading[key] = true
	delete(l.ResourceErrors, key)
	epoch, ctx, b := l.epoch, l.hostCtx, l.Client
	go func() {
		var v any
		var err error
		if b == nil {
			err = errors.New("尚未连接运行主机")
		} else {
			err = b.request(ctx, method, path, body, &v)
		}
		a.post(epoch, func() {
			l.Loading[key] = false
			if err != nil {
				a.reportLiveError(err)
				l.ResourceErrors[key] = l.Error
				return
			}
			if !l.CredentialRepair {
				l.Error = ""
			}
			if done != nil {
				done(v)
			}
		})
	}()
}
func (a *preview) loadSettingsPage() {
	l := a.Live
	if a.Settings.Tab == "models" && a.Settings.host().Local && (a.Settings.Mode == SettingCopyEasyCodeMode || l.Config.Model.Mode == "easycode") {
		a.refreshEasyCodeStatus()
		if l.Config.Model.Mode == "easycode" {
			a.refreshEasyCodeModels()
		}
	}
	if a.Settings.Tab == "models" && a.Settings.host().Local {
		a.refreshCodexStatus()
	}
	if l.Client == nil {
		return
	}
	s := &a.Settings
	switch s.Tab {
	case "workflows":
		a.resource("workflows", "/dynamic-workflows")
	case "hooks":
		a.loadHookSettings(false)
	case "remote-access":
		if s.host().Local {
			a.resource("remote-access", "/remote-access")
		}
	case "permissions":
		a.resource("permissions", "/settings/permissions")
		if !l.Loading["tool-discovery-save"] {
			a.resource("tool-discovery", "/settings/tool-discovery")
		}
		if !l.Loading["builtin-tools-save"] {
			a.resource("builtin-tools", "/settings/builtin-tools")
		}
	case "agents":
		a.refreshTeams()
		if s.Business.ExpertTab == "market" {
			a.loadExpertMarket()
		}
	case "memory-knowledge":
		a.refreshBusiness()
		if s.Business.Tab == "embedding" {
			s.Business.EmbeddingLoaded = false
			a.operation("embedding-load", "GET", "/knowledge/embeddings", nil, func(v any) { s.Business.setEmbedding(v) })
		}
		if s.Business.Tab == "memories" {
			a.loadAgentMemories()
		}
	case "teams":
		a.refreshTeams()
	case "models":
		a.refreshModels()
	case "usage":
		a.resource("usage", "/usage/summary")
	case "experiments":
		a.resource("computer", "/computer/settings")
	case "mcp":
		a.loadMCPStatus(false)
	case "capabilities":
		a.refreshSkillLists()
	case "feishu":
		a.resource("feishu", "/settings/feishu")
		a.resource("permissions", "/settings/permissions")
	case "skillMarket":
		a.searchMarket()
		a.marketRead("installed", "/skills/market/installed", nil)
		a.marketRead("sections", "/skills/market/sections", nil)
	}
}
func (a *preview) saveLiveDefaultModel(model string) {
	if strings.TrimSpace(model) == "" {
		return
	}
	currentInfo := object(object(a.Live.Data["models"])["current"])
	provider := str(currentInfo["provider"])
	payload := map[string]any{
		"model": strings.TrimSpace(model),
	}
	if provider != "" {
		payload["provider"] = provider
	}
	a.operation("model-default", "PUT", "/models/default", payload, func(v any) {
		a.Settings.message("已保存主机默认模型（需重启核心生效）")
		a.refreshModels()
	})
}

func (a *preview) liveSettingsPage(c *ui.Context) {
	l := a.Live
	tab := a.Settings.Tab
	if tab != "speech" && tab != "hooks" && tab != "permissions" && tab != "agents" && tab != "teams" && tab != "memory-knowledge" && tab != "appearance" && tab != "general" && tab != "connections" && tab != "models" && tab != "capabilities" && tab != "mcp" {
		ui.Row(c).Children(func() {
			a.note(c, a.connectionLabel())
			ui.Spacer(c)
			a.action(c, "刷新").OnClick(a.loadSettingsPage)
		})
	}
	switch tab {
	case "workflows":
		a.liveWorkflowSettings(c)
	case "hooks":
		a.liveHookSettings(c)
	case "remote-access":
		a.liveRemoteAccessSettings(c)
	case "agents":
		a.businessSettings(c)
	case "teams":
		a.teamSettings(c)
	case "memory-knowledge":
		a.memoryKnowledgeSettings(c)
	case "permissions":
		a.livePermissionSettings(c)
		a.liveBuiltinToolSettings(c)
		a.toolDiscoverySettings(c)
	case "general":
		a.generalSettings(c)
		a.note(c, "会话、草稿、主机和模型配置会保存到此原生应用的数据目录。模型 API Key 与主机密钥保存在 macOS 钥匙串；EasyCode 登录凭据保存在仅当前用户可访问的目录。")
		a.action(c, "打开应用数据目录").AlignSelf(ui.Start).OnClick(func() { go mygo.Shell.OpenPath(l.Dir) })
	case "appearance":
		a.appearanceSettings(c)
	case "speech":
		a.speechSettings(c)
	case "models":
		a.liveModelSettings(c)
	case "connections":
		a.liveConnectionSettings(c)
	case "usage":
		a.liveUsageSettings(c)
	case "experiments":
		a.liveComputerSettings(c)
	case "mcp":
		a.liveMCPSettings(c)
	case "capabilities":
		a.liveCapabilitySettings(c)
	case "skillMarket":
		a.liveMarketSettings(c)
	case "feishu":
		a.livePermissionSettings(c)
		a.liveFeishuSettings(c)
	}
}
func (a *preview) liveModelSettings(c *ui.Context) {
	s := &a.Settings
	l := a.Live
	busy := l.configBusy() || l.Loading["model-check"] || l.Loading["model-default"]
	ui.Row(c).Children(func() {
		a.heading(c, SettingCopyGlobalModelConfig+a.dirtySuffix())
		ui.Spacer(c)
		a.action(c, SettingCopyRefreshModels).Disabled(busy || l.Loading["model-list"]).OnClick(a.refreshModels)
	})
	currentInfo := object(object(l.Data["models"])["current"])
	defaultModel := str(currentInfo["id"])
	if defaultModel == "" {
		defaultModel = "尚未读取"
	}
	if s.DefaultModelChoice == "" && defaultModel != "尚未读取" {
		s.DefaultModelChoice = defaultModel
	}
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).Gap(12).Wrap().AlignItems(ui.Center).Children(func() {
			ui.Text(c, a.tr(SettingCopyDefaultModel)).FontSize(13).FontWeight(500)
			if len(l.Models) > 0 {
				choices := l.Models
				if s.DefaultModelChoice == "" {
					s.DefaultModelChoice = choices[0]
				}
				a.selectControl(c.Key("host-default-model"), "选择主机默认模型", &s.DefaultModelChoice, choices).Width(280).MaxWidth(320).Grow(1).Shrink(1).MinWidth(0).Height(32).Disabled(busy)
			} else {
				a.input(c, "", &s.DefaultModelChoice, "填写模型 ID", false).Label("主机默认模型")
			}
			a.primary(c, SettingCopySaveDefaultModel).Disabled(busy || strings.TrimSpace(s.DefaultModelChoice) == "").OnClick(func() {
				a.saveLiveDefaultModel(s.DefaultModelChoice)
			})
		})
		if discoveryError := str(object(l.Data["models"])["discovery_error"]); discoveryError != "" {
			a.note(c, "模型拉取失败: "+discoveryError)
		}
	})
	a.preferenceRow(c, SettingCopyCurrentModel, a.current().Model, func() {})
	a.note(c, SettingCopyModelNote)
	if !s.host().Local {
		a.note(c, SettingCopyRemoteModelNote)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			for index, h := range l.Config.Hosts {
				selectedButton(c, a.action(c, h.Name), h.ID == l.Config.ActiveHost).Disabled(l.Connecting).OnClick(func() { a.selectLiveHost(index) })
			}
		})
		return
	}
	a.heading(c, "模型服务")
	a.modelSourceSelector(c, busy)
	if s.ModelShowChatGPT {
		a.codexSettings(c)
		return
	}
	if s.Mode == SettingCopyDesktopMode {
		a.savedModelProfiles(c)
	}
	ui.Column(c).Gap(12).Disabled(busy).Children(func() {
		if s.Mode == "在桌面客户端配置" {
			ui.Row(c).Gap(16).Wrap().Children(func() {
				for index := 0; index < 4; index++ {
					ui.Column(c.Key(index)).Basis(220).Grow(1).MinWidth(180).Children(func() {
						switch index {
						case 0:
							if a.dropdown(c, "模型服务", &s.Provider, []string{"OpenAI 兼容服务 / 网关", SettingCopyOpenAIProvider, "Anthropic", SettingCopyZhipuProvider, SettingCopyVolcProvider}).Changed() {
								s.APIKey = ""
								a.clearModelCheck()
								// 切到智谱时把仍是各家默认占位的地址替换为 coding 端点，已有自定义地址不动
								if s.Provider == SettingCopyZhipuProvider || s.Provider == SettingCopyVolcProvider || s.Provider == SettingCopyOpenAIProvider {
									switch s.BaseURL {
									case "", "http://localhost:4001", "https://api.anthropic.com", SettingCopyZhipuBaseURL, SettingCopyVolcBaseURL, SettingCopyOpenAIBaseURL:
										if s.Provider == SettingCopyZhipuProvider {
											s.BaseURL = SettingCopyZhipuBaseURL
										} else if s.Provider == SettingCopyVolcProvider {
											s.BaseURL = SettingCopyVolcBaseURL
										} else {
											s.BaseURL = SettingCopyOpenAIBaseURL
										}
									}
								}
							}
						case 1:
							placeholder := "http://localhost:4001"
							switch s.Provider {
							case "Anthropic":
								placeholder = "https://api.anthropic.com"
							case SettingCopyZhipuProvider:
								placeholder = SettingCopyZhipuBaseURL
							case SettingCopyVolcProvider:
								placeholder = SettingCopyVolcBaseURL
							case SettingCopyOpenAIProvider:
								placeholder = SettingCopyOpenAIBaseURL
							}
							if a.input(c, "服务地址", &s.BaseURL, placeholder, false).Label("全局模型服务地址").Changed() {
								a.clearModelCheck()
							}
						case 2:
							placeholder := "填写模型 ID，或检测连接后选择"
							if s.Provider == SettingCopyZhipuProvider {
								placeholder = "填写模型 ID，或从下方常用模型选择"
							}
							a.input(c, "默认模型", &s.Model, placeholder, false).Label("全局默认模型")
						case 3:
							placeholder := "首次配置请输入 API Key"
							provider := "openai"
							if s.Provider == "Anthropic" {
								provider = "anthropic"
							}
							target := modelConfig{Provider: provider, BaseURL: s.BaseURL}
							profile, saved := availableModelProfiles(l.Config)[modelProfileID(target)]
							if l.Config.Model.HasKey && sameModelCredentialTarget(l.Config.Model, target) || saved && profile.HasKey && sameModelCredentialTarget(profile, target) {
								placeholder = "留空保留已存入钥匙串的密钥"
							}
							if a.input(c, "API Key", &s.APIKey, placeholder, true).Changed() {
								a.clearModelCheck()
							}
						}
					})
				}
			})
			nativeDisclosure(c, "reasoning-settings", "思考与流式响应", func() {
				choices := []string{"沿用模型默认", "开启思考", "关闭思考"}
				if s.Provider == "Anthropic" {
					choices = append(choices, "自适应思考")
				}
				a.dropdown(c, "思考模式", &s.Thinking, choices)
				if s.Provider == "Anthropic" && thinkingMode(s.Thinking) == "enabled" {
					a.input(c, "思考预算（token）", &s.ThinkingBudget, "至少 1024，例如 4096", false)
				}
				if s.Provider != "Anthropic" {
					a.dropdown(c, "推理强度", &s.ReasoningEffort, []string{"沿用模型默认", "none", "minimal", "low", "medium", "high", "xhigh", "max"})
				}
				a.input(c, "流空闲超时", &s.StreamIdleTimeout, "默认 300s；例如 5m", false)
				a.note(c, "留空沿用模型默认；仅对支持的模型开启思考，可能增加耗时和 token。adaptive 仅供支持它的 Anthropic 模型使用。")
			})
			a.note(c, "配置适用于此 Mac 的本地 Agent。密钥保存在 macOS 钥匙串；远程 Agent 的访问令牌在运行主机中配置。")
			if s.Provider == SettingCopyOpenAIProvider {
				a.note(c, "OpenAI API Key 使用 API 账户计费；ChatGPT 订阅登录请使用下方登录入口。")
			}
		} else if s.Mode == SettingCopyEasyCodeMode {
			a.note(c, "使用飞书登录 EasyCode，无需单独配置网关；模型使用所登录账号的额度。")
		} else {
			a.note(c, "读取 ~/.easyagent/config.yaml、~/.easyagent/.env 和当前环境中的模型与工具配置。修改后点击应用即可重新读取。")
			a.action(c, "配置全局模型").AlignSelf(ui.Start).OnClick(func() {
				s.Mode = "在桌面客户端配置"
				if s.Model == "" {
					s.Model = str(object(object(l.Data["models"])["current"])["id"])
				}
				a.clearModelCheck()
			})
		}
	})
	if s.Mode == SettingCopyEasyCodeMode {
		a.easyCodeSettings(c)
	}
	running := false
	for _, session := range a.Sessions {
		running = running || session.Running || session.Pending || session.Sending
	}
	missingURL := s.Mode == "在桌面客户端配置" && strings.TrimSpace(s.BaseURL) == ""
	incomplete := missingURL || s.Mode == "在桌面客户端配置" && strings.TrimSpace(s.Model) == "" || s.Mode == SettingCopyEasyCodeMode && (!l.EasyCode.LoggedIn || strings.TrimSpace(s.Model) == "")
	ui.Row(c).Gap(12).Wrap().Margin(12, 0, 0, 0).Children(func() {
		a.action(c, "检测连接").Disabled(busy || missingURL).OnClick(a.checkLiveModel)
		a.primary(c, "应用并重新连接").Disabled(busy || l.Connecting || incomplete || running).OnClick(a.applyLiveModel)
		a.action(c, "撤销未保存修改").Disabled(busy).OnClick(func() { a.syncSettingsConfig(); s.APIKey = ""; a.clearModelCheck() })
	})
	if busy {
		a.note(c, "正在处理模型配置…")
	}
	if running {
		a.note(c, "当前有任务正在执行，完成或停止任务后即可应用配置。")
	}
	a.note(c, "检测只读取模型目录，不发送推理请求。应用后新会话使用新的默认模型，已有会话的单独选择保留。")
	if catalog := object(l.Data["model-check"]); len(catalog) > 0 {
		a.heading(c, "检测到的模型")
		a.note(c, str(catalog["message"]))
		a.input(c, "搜索检测到的模型", &s.ModelCatalogSearch, "按模型 ID 搜索", false)
		query := strings.ToLower(s.ModelCatalogSearch)
		var names []string
		for _, item := range list(catalog["data"]) {
			name := str(object(item)["id"])
			if strings.Contains(strings.ToLower(name), query) {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			a.note(c, "没有匹配的模型")
		}
		ui.List(c.Key(query), nil, len(names)).Label("默认模型目录").MaxHeight(260).Shrink(0).
			ItemKey(func(i int) any { return names[i] }).Rows(func(row ui.ListRow) {
			c, name := row.Context, names[row.Index]
			menuOption(c.Key(name), "cpu", name, s.Model == name).Label("设为默认模型：" + name).Disabled(busy || s.Mode != "在桌面客户端配置").OnClick(func() { s.Model = name })
		})
	}
	if s.Mode == "在桌面客户端配置" && s.Provider == SettingCopyZhipuProvider {
		a.heading(c, "常用模型")
		a.note(c, SettingCopyZhipuModelNote)
		ui.List(c.Key("zhipu-presets"), nil, len(zhipuModelPresets)).Label("智谱常用模型").MaxHeight(180).Shrink(0).
			ItemKey(func(i int) any { return zhipuModelPresets[i] }).Rows(func(row ui.ListRow) {
			c, name := row.Context, zhipuModelPresets[row.Index]
			menuOption(c.Key(name), "cpu", name, s.Model == name).Label("设为默认模型：" + name).Disabled(busy).OnClick(func() { s.Model = name })
		})
	}
	if catalog := object(l.Data["models"]); str(catalog["discovery_error"]) != "" {
		a.note(c, "模型目录读取失败；当前配置的模型仍可选择。")
	}
}

func modelSource(form modelForm) string {
	if form.Mode == SettingCopyEasyCodeMode {
		return "easycode"
	}
	if form.Mode != SettingCopyDesktopMode {
		return "inherit"
	}
	switch form.Provider {
	case SettingCopyOpenAIProvider:
		return "openai"
	case SettingCopyZhipuProvider:
		return "zhipu"
	case SettingCopyVolcProvider:
		return "volc"
	default:
		return "custom"
	}
}

func availableModelProfiles(cfg clientConfig) map[string]modelConfig {
	profiles := cloneConfig(cfg).ModelProfiles
	if profiles == nil {
		profiles = map[string]modelConfig{}
	}
	add := func(p modelConfig) {
		if isModelOverride(p) && p.HasKey {
			id := modelProfileID(p)
			if _, exists := profiles[id]; !exists {
				profiles[id] = p
			}
		}
	}
	add(cfg.Model)
	if cfg.PreviousModel != nil {
		add(*cfg.PreviousModel)
	}
	return profiles
}

func (a *preview) selectModelSource(source string) {
	s, l := &a.Settings, a.Live
	if source == "chatgpt" {
		s.ModelShowChatGPT = true
		return
	}
	if modelSource(s.modelForm) == source {
		s.ModelShowChatGPT = false
		return
	}
	change := func() {
		choice := s.DefaultModelChoice
		s.modelForm = l.SavedModelForm
		s.ModelShowChatGPT = false
		s.APIKey = ""
		switch source {
		case "inherit":
			s.Mode = SettingCopyCmdlineMode
		case "easycode":
			s.Mode = SettingCopyEasyCodeMode
			if l.Config.Model.Mode != "easycode" {
				s.Model = ""
			}
			a.refreshEasyCodeStatus()
			if l.EasyCode.LoggedIn {
				a.refreshEasyCodeModels()
			}
		default:
			provider, base := "OpenAI 兼容服务 / 网关", ""
			switch source {
			case "openai":
				provider, base = SettingCopyOpenAIProvider, SettingCopyOpenAIBaseURL
			case "zhipu":
				provider, base = SettingCopyZhipuProvider, SettingCopyZhipuBaseURL
			case "volc":
				provider, base = SettingCopyVolcProvider, SettingCopyVolcBaseURL
			}
			candidate := modelConfig{Mode: "override", Provider: "openai", BaseURL: base}
			if p, ok := availableModelProfiles(l.Config)[modelProfileID(candidate)]; base != "" && ok && sameModelCredentialTarget(p, candidate) {
				s.modelForm = modelFormFromConfig(p)
			} else if modelSource(l.SavedModelForm) == source {
				s.modelForm = l.SavedModelForm
			} else {
				s.modelForm = modelForm{Mode: SettingCopyDesktopMode, Provider: provider, BaseURL: base, Thinking: "沿用模型默认", ReasoningEffort: "沿用模型默认"}
			}
		}
		s.DefaultModelChoice = choice
		a.clearModelCheck()
	}
	if a.settingsFormDirty() {
		a.requestConfirmation("切换模型配置", "当前有未保存的模型设置，切换将丢弃这些修改。", change)
		return
	}
	change()
}

func (a *preview) modelSourceSelector(c *ui.Context, busy bool) {
	s := &a.Settings
	selected := modelSource(s.modelForm)
	if s.ModelShowChatGPT {
		selected = "chatgpt"
	}
	for _, group := range []struct {
		title   string
		entries [][2]string
	}{
		{"账号登录", [][2]string{{"easycode", "EasyCode 飞书登录"}, {"chatgpt", "ChatGPT 订阅登录"}}},
		{"API 与 Coding Plan", [][2]string{{"openai", SettingCopyOpenAIProvider}, {"zhipu", SettingCopyZhipuProvider}, {"volc", SettingCopyVolcProvider}, {"custom", "其他服务"}, {"inherit", "沿用命令行配置"}}},
	} {
		a.note(c, group.title)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			for _, entry := range group.entries {
				id, label := entry[0], entry[1]
				selectedButton(c.Key(id), a.action(c.Key(id), label), selected == id).Disabled(busy).OnClick(func() { a.selectModelSource(id) })
			}
		})
	}
}

func (a *preview) savedModelProfiles(c *ui.Context) {
	profiles := availableModelProfiles(a.Live.Config)
	if len(profiles) == 0 {
		return
	}
	ids := make([]string, 0, len(profiles))
	for id, p := range profiles {
		if isModelOverride(p) && p.HasKey && id == modelProfileID(p) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return
	}
	a.heading(c, "已保存的模型服务")
	a.note(c, "各服务的密钥分别保存；选择后可检查配置并应用为当前主机的模型服务。")
	ui.Row(c).Gap(8).Wrap().Children(func() {
		for _, id := range ids {
			p := profiles[id]
			label := modelFormFromConfig(p).Provider + " · " + p.Model
			if u, err := url.Parse(p.BaseURL); err == nil && u.Host != "" {
				label += " · " + u.Host
			}
			load := func() {
				choice := a.Settings.DefaultModelChoice
				a.Settings.modelForm = modelFormFromConfig(p)
				a.Settings.DefaultModelChoice = choice
				a.clearModelCheck()
			}
			a.action(c.Key(id), label).MaxWidth(320).Disabled(a.Live.configBusy()).OnClick(func() {
				if a.settingsFormDirty() {
					a.requestConfirmation("切换模型配置", "当前有未保存的模型设置，切换将丢弃这些修改。", load)
					return
				}
				load()
			})
		}
	})
}
func (a *preview) clearModelCheck() {
	delete(a.Live.Data, "model-check")
	a.Settings.ModelCatalogSearch = ""
	a.Live.Versions["model-check"]++
}

func (a *preview) modelCandidate() (modelConfig, string, error) {
	return a.modelCandidateWithModel(a.Settings.Model)
}
func (a *preview) modelCandidateWithModel(model string) (modelConfig, string, error) {
	s := &a.Settings
	cfg := modelConfig{Mode: "inherit"}
	if s.Mode == "在桌面客户端配置" {
		provider := "openai"
		if s.Provider == "Anthropic" {
			provider = "anthropic"
		}
		cfg = modelConfig{Thinking: thinkingMode(s.Thinking), ReasoningEffort: reasoningEffort(s.ReasoningEffort), ThinkingBudget: s.ThinkingBudget, StreamIdleTimeout: s.StreamIdleTimeout, Mode: "override", Provider: provider, BaseURL: s.BaseURL, Model: model, HasKey: true}
	}
	cfg, err := validatedModel(cfg)
	key := strings.TrimSpace(s.APIKey)
	if strings.ContainsAny(key, " \t\r\n") || len(key) > 8192 {
		err = errors.New("模型密钥包含无效字符")
	}
	return cfg, key, err
}

// Catalog discovery needs a connection, but not a model selected in advance.
func (a *preview) modelCandidateForCheck() (modelConfig, string, error) {
	model := a.Settings.Model
	if strings.TrimSpace(model) == "" {
		model = "catalog-discovery"
	}
	return a.modelCandidateWithModel(model)
}

// A stored key belongs to the configured endpoint, not every compatible API.
func sameModelCredentialTarget(old, next modelConfig) bool {
	return old.Provider == next.Provider && strings.TrimRight(strings.TrimSpace(old.BaseURL), "/") == strings.TrimRight(strings.TrimSpace(next.BaseURL), "/")
}

func savedModelCredential(cfg modelConfig, current modelConfig, profiles map[string]modelConfig, secrets secretStore) (string, error) {
	if p, ok := profiles[modelProfileID(cfg)]; ok && isModelOverride(p) && p.HasKey && sameModelCredentialTarget(p, cfg) {
		return secrets.Get(modelSecretName(p))
	}
	if isModelOverride(current) && current.HasKey && sameModelCredentialTarget(current, cfg) {
		return secrets.Get(modelSecretName(current))
	}
	return "", errors.New("切换模型服务地址或类型时需要填写密钥")
}

func (a *preview) checkLiveModel() {
	l := a.Live
	if a.Settings.Mode == SettingCopyEasyCodeMode {
		a.refreshEasyCodeModels()
		return
	}
	if l.configBusy() || l.Loading["model-check"] {
		return
	}
	cfg, key, err := a.modelCandidateForCheck()
	if err != nil {
		l.Error = err.Error()
		return
	}
	if cfg.Mode == "inherit" {
		a.refreshModels()
		l.Notice = "已读取核心当前配置的模型目录；未执行推理请求。"
		return
	}
	a.clearModelCheck()
	version := l.Versions["model-check"]
	l.Loading["model-check"] = true
	epoch, ctx, secrets := l.epoch, l.ctx, l.Secrets
	oldModel := l.Config.Model
	if oldModel.Mode == "easycode" && l.Config.PreviousModel != nil {
		oldModel = *l.Config.PreviousModel
	}
	profiles := cloneConfig(l.Config).ModelProfiles
	go func() {
		var err error
		if key == "" {
			key, err = savedModelCredential(cfg, oldModel, profiles, secrets)
		}
		var result map[string]any
		if err == nil && key == "" {
			err = errors.New("请输入模型密钥")
		}
		if err == nil {
			b, _ := newBackendClient(cfg.BaseURL, key)
			if cfg.Provider == "anthropic" {
				req, _ := newModelRequest(ctx, b, key)
				err = b.execute(req, &result)
			} else {
				err = b.request(ctx, "GET", modelCatalogPath(b.URL), nil, &result)
			}
		}
		if err == nil {
			rows, ok := result["data"].([]any)
			if !ok {
				err = errors.New("模型目录响应格式无效")
			} else {
				clean := []any{}
				for _, raw := range rows {
					item := object(raw)
					id := str(item["id"])
					if id == "" || len(id) > 512 || strings.ContainsAny(id, "\r\n\t") {
						continue
					}
					if key != "" && strings.Contains(id, key) {
						err = errors.New("模型服务返回了无效目录")
						break
					}
					clean = append(clean, map[string]any{"id": id})
				}
				result = map[string]any{"data": clean}
			}
		}
		a.post(epoch, func() {
			l.Loading["model-check"] = false
			if version != l.Versions["model-check"] {
				return
			}
			if err != nil {
				l.Error = err.Error()
				return
			}
			result["message"] = "已连接模型目录，未执行推理请求。"
			l.Data["model-check"] = result
			l.Error = ""
		})
	}()
}
func (a *preview) applyLiveModel() {
	l := a.Live
	if a.Settings.Mode == SettingCopyEasyCodeMode {
		a.applyEasyCodeModel(a.Settings.Model)
		return
	}
	if l.configBusy() || l.Loading["model-check"] {
		l.Error = "正在保存配置，请稍后再试"
		return
	}
	cfg, key, err := a.modelCandidate()
	if err != nil {
		l.Error = err.Error()
		return
	}
	for _, s := range a.Sessions {
		if s.Running || s.Sending || s.Pending {
			l.Error = "请先等待或停止正在运行的任务，再应用模型配置"
			return
		}
	}
	l.Loading["model-save"] = true
	epoch, ctx, secrets, dir := l.epoch, l.ctx, l.Secrets, l.Dir
	old := cloneConfig(l.Config)
	next := cloneConfig(old)
	previousModel := old.Model
	if previousModel.Mode == "easycode" && old.PreviousModel != nil {
		previousModel = *old.PreviousModel
	}
	go func() {
		var err error
		var rollbackErr error
		var replacedKey string
		var hadStoredKey, keySaved bool
		var configWritten, coreStopped bool
		var targetSecret string
		if err = l.Core.ensureIdle(ctx); err != nil {
			a.post(epoch, func() { l.Loading["model-save"] = false; l.Error = err.Error() })
			return
		}
		if next.ModelProfiles == nil {
			next.ModelProfiles = map[string]modelConfig{}
		}
		if isModelOverride(previousModel) && previousModel.HasKey {
			id := modelProfileID(previousModel)
			if _, exists := next.ModelProfiles[id]; !exists {
				next.ModelProfiles[id] = previousModel
			}
		}
		if cfg.Mode == "override" {
			if key == "" {
				key, err = savedModelCredential(cfg, previousModel, old.ModelProfiles, secrets)
			}
			if err == nil && key == "" {
				err = errors.New("请输入模型密钥")
			}
			cfg.ProfileSecret = true
			cfg.Mode = "profile"
			targetSecret = modelSecretName(cfg)
			if err == nil {
				replacedKey, err = secrets.Get(targetSecret)
				hadStoredKey = replacedKey != ""
			}
			if err == nil {
				keySaved = true // Set may write successfully before reporting an error.
				err = secrets.Set(targetSecret, key)
				if err == nil {
					next.ModelProfiles[modelProfileID(cfg)] = cfg
				}
			}
		}
		next.Model = cfg
		if err == nil {
			err = atomicJSON(filepath.Join(dir, "connections.json"), next)
			configWritten = err == nil
		}
		if err == nil {
			l.Core.stop()
			coreStopped = true
			_, err = l.Core.start(ctx, dir, cfg, secrets)
		}
		if err != nil {
			if keySaved && hadStoredKey {
				rollbackErr = errors.Join(rollbackErr, secrets.Set(targetSecret, replacedKey))
			} else if keySaved {
				rollbackErr = errors.Join(rollbackErr, secrets.Delete(targetSecret))
			}
			if configWritten {
				rollbackErr = errors.Join(rollbackErr, atomicJSON(filepath.Join(dir, "connections.json"), old))
			}
			if coreStopped {
				l.Core.stop()
				_, restartErr := l.Core.start(ctx, dir, old.Model, secrets)
				rollbackErr = errors.Join(rollbackErr, restartErr)
			}
		}
		a.post(epoch, func() {
			l.Loading["model-save"] = false
			if err != nil {
				a.connectLive()
				if rollbackErr != nil {
					l.Error = "应用失败：" + err.Error() + "；恢复原配置失败，请检查连接设置：" + rollbackErr.Error()
				} else {
					l.Error = "应用失败，已恢复原配置：" + err.Error()
				}
				return
			}
			a.Settings.APIKey = ""
			l.Config = next
			a.syncSettingsConfig()
			a.clearModelCheck()
			l.Notice = "全局模型配置已保存，新建会话将使用新的默认模型。"
			a.connectLive()
		})
	}()
}
func (a *preview) liveConnectionSettings(c *ui.Context) {
	l := a.Live
	s := &a.Settings
	if phoneClient {
		a.phonePairView(c)
	}
	if phoneClient {
		a.note(c, "已连接的电脑；会话和文件保存在对应电脑上。")
	} else {
		a.note(c, "管理本机与远程连接。会话、文件和工具保存在各自主机上。")
	}
	for index, h := range l.Config.Hosts {
		ui.Row(c.Key(h.ID)).Gap(10).AlignItems(ui.Center).Padding(16, 0).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				a.text(c, h.Name).Bold()
				value := h.URL
				if h.Local {
					value = "应用管理的本地核心"
				} else if h.Relay != nil {
					value = "加密中继连接"
				}
				a.note(c, value)
			})
			label := "连接"
			if h.ID == l.Config.ActiveHost {
				label = a.connectionLabel()
			}
			a.action(c, label).Disabled(l.Connecting).OnClick(func() { a.selectLiveHost(index) })
			if !h.Local {
				a.action(c, "编辑").OnClick(func() {
					s.hostForm = hostForm{EditHost: h.ID, HostName: h.Name, HostURL: h.URL}
					a.PhoneHostEditor = true
				})
				a.danger(c, "移除").OnClick(func() {
					l.Confirm = &nativeConfirmation{"移除主机连接？", h.Name + "；不会删除主机上的会话或文件。", func() { a.removeLiveHost(index) }}
				})
			}
		})
	}
	if phoneClient && !a.PhoneHostEditor {
		a.action(c, "手动添加连接").AlignSelf(ui.Start).OnClick(func() { a.PhoneHostEditor = true; s.hostForm = hostForm{} })
	}
	if !phoneClient || a.PhoneHostEditor {
		a.heading(c, "添加或修改远程主机"+a.dirtySuffix())
		a.input(c, "连接名称", &s.HostName, "例如 迷你主机", false)
		a.input(c, "服务地址", &s.HostURL, "http://192.168.5.16:8080", false)
		a.input(c, "访问令牌", &s.HostToken, "原地址留空保留令牌；新连接留空表示无令牌", true).Bind(&s.HostTokenField)
		ui.Row(c).Gap(8).Children(func() {
			a.primary(c, "保存连接").Disabled(l.Loading["host-save"]).OnClick(a.saveLiveHost)
			if a.settingsFormDirty() {
				a.action(c, "放弃修改").OnClick(a.discardSettingsForms)
			}
			a.action(c, "新增连接").OnClick(func() { s.hostForm = hostForm{} })
		})
	}

	a.action(c, "重连当前主机").AlignSelf(ui.Start).Disabled(l.Connecting).OnClick(a.connectLive)
}
func (a *preview) selectLiveHost(index int) {
	l := a.Live
	if a.Settings.Business.dirty() {
		a.businessNavigate(func() { a.selectLiveHost(index) })
		return
	}
	if l.configBusy() {
		l.Error = "正在保存配置，请稍后切换主机"
		return
	}
	if index < 0 || index >= len(l.Config.Hosts) {
		return
	}
	a.captureDraftInputs()
	for _, s := range a.Sessions {
		l.Drafts[l.Config.ActiveHost+"/"+s.RemoteID] = s.Draft
	}
	l.Config.ActiveHost = l.Config.Hosts[index].ID
	a.Settings.Host = index
	l.Data = map[string]any{}
	l.Loading = map[string]bool{}
	l.ResourceErrors = map[string]string{}
	l.QRCode = ""
	l.QRImage = nil
	l.LoginName = ""
	l.Confirm = nil
	l.Rename = false
	l.RenameOwner = nil
	l.EditQueueID = ""
	l.EditQueueOwner = nil
	a.Settings.APIKey = ""
	a.Settings.Business = businessSettingsState{}
	a.Settings.feishuForm = feishuForm{}
	a.Settings.mcpForm = mcpForm{Workspace: l.workspace()}
	a.Live.SavedMCPForm = a.Settings.mcpForm
	a.Settings.hostForm = hostForm{}
	a.Sessions = []*session{{ID: a.NextID, Title: "新会话", UpdatedAt: time.Now().UTC().Format(time.RFC3339), Workspace: l.workspace(), Draft: l.Drafts[l.Config.ActiveHost+"/"]}}
	a.Active = a.NextID
	a.NextID++
	a.restoreDraftInputs(a.current())
	a.syncSettingsConfig()
	a.connectLive()
	next := cloneConfig(l.Config)
	version := l.Store.reserve("connections.json")
	a.run("configuration", func(context.Context, *backendClient) (any, error) {
		return nil, l.Store.save("connections.json", version, next)
	}, nil)
}
func (a *preview) saveLiveHost() {
	l := a.Live
	if l.configBusy() {
		l.Error = "正在保存配置，请稍后再试"
		return
	}
	s := &a.Settings
	if strings.TrimSpace(s.HostName) == "" {
		l.Error = "请输入连接名称"
		return
	}
	_, err := newBackendClient(s.HostURL, s.HostToken)
	if err != nil {
		l.Error = err.Error()
		return
	}
	form, old := s.hostForm, cloneConfig(l.Config)
	wasActive := form.EditHost == old.ActiveHost
	l.Loading["host-save"] = true
	epoch := l.epoch
	go func() {
		saved, err := persistHostProfile(l.Dir, l.Secrets, old, form)
		a.post(epoch, func() {
			l.Loading["host-save"] = false
			if err != nil {
				a.reportLiveError(err)
				return
			}
			l.Config = saved.Config
			s.EditHost = saved.Config.Hosts[saved.Index].ID
			s.HostToken = ""
			s.RepairCredential = false
			a.syncSettingsConfig()
			if !l.CredentialRepair {
				l.Error = ""
			}
			l.Notice = "主机连接已保存。"
			if saved.NewIdentity {
				l.Notice = "已另存为新连接，原连接及其草稿仍保留。"
			}
			if wasActive && saved.NewIdentity {
				a.selectLiveHost(saved.Index)
			} else if wasActive && l.CredentialRepair {
				a.connectLive()
			}
		})
	}()
}

func (a *preview) removeLiveHost(index int) {
	l := a.Live
	if l.configBusy() {
		l.Error = "正在保存配置，请稍后再试"
		return
	}
	if index < 0 || index >= len(l.Config.Hosts) || l.Config.Hosts[index].Local {
		return
	}
	old := cloneConfig(l.Config)
	id := old.Hosts[index].ID
	active := old.ActiveHost == id
	a.run("configuration", func(context.Context, *backendClient) (any, error) {
		return persistHostRemoval(l.Dir, l.Secrets, old, index)
	}, func(value any) {
		removed := value.(removedHostProfile)
		l.Config = removed.Config
		if a.Settings.EditHost == id {
			a.Settings.hostForm = hostForm{}
			a.Settings.HostTokenField.CancelFocus()
		}
		if active {
			// Keep the source identity until switching has captured its drafts.
			l.Config.ActiveHost = id
			a.selectLiveHost(0)
		} else {
			a.syncSettingsConfig()
		}
		if !l.CredentialRepair {
			l.Error = ""
		}
		l.Notice = "主机连接已移除。"
		if removed.CleanupError != nil {
			l.Notice = "连接已移除，但系统钥匙串中的旧访问令牌未能清理。"
		}
	})
}

func (a *preview) liveUsageSettings(c *ui.Context) {
	if a.resourceFeedback(c, "usage", a.loadSettingsPage) {
		return
	}
	m := object(a.Live.Data["usage"])
	total := object(m["total"])
	total["total_tokens"] = number(total["input_tokens"]) + number(total["output_tokens"])
	a.heading(c, "近 30 天用量概览")
	a.note(c, "基于核心持久化的执行收据统计，包含多轮工具调用与子 Agent 用量。")
	// Without a connection the totals would read as zero, which reads as
	// "no usage" — show an explicit placeholder instead.
	format := formatCount
	if !a.Live.Connected {
		format = func(any) string { return "—" }
	}
	ui.Row(c).Gap(12).Children(func() {
		for _, item := range []struct{ label, key string }{{"总 Tokens", "total_tokens"}, {"输入 Tokens", "input_tokens"}, {"输出 Tokens", "output_tokens"}, {"总任务数 (Runs)", "run_count"}} {
			ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(8).Padding(12).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(8).Children(func() { a.note(c, item.label); ui.Text(c, format(total[item.key])).FontSize(20).Bold() })
		}
	})
	child := object(total["subagent_usage"])
	a.note(c, "其中子 Agent · 输入 "+format(child["input_tokens"])+" · 输出 "+format(child["output_tokens"]))
	a.heading(c, "会话用量排行")
	for _, raw := range list(m["sessions"]) {
		row := object(raw)
		id := str(row["session_id"])
		title := id
		for _, s := range a.Sessions {
			if s.RemoteID == id {
				title = s.Title
			}
		}
		a.preferenceRow(c, title, fmt.Sprintf("%s 次任务 · 输入 %s · 输出 %s · 合计 %s", formatCount(row["run_count"]), formatCount(row["input_tokens"]), formatCount(row["output_tokens"]), formatCount(number(row["input_tokens"])+number(row["output_tokens"]))), func() {
			a.action(c, "打开会话").OnClick(func() {
				for _, s := range a.Sessions {
					if s.RemoteID == id {
						a.closeSettings()
						a.selectSession(s.ID)
					}
				}
			})
		})
	}
	if len(list(m["sessions"])) == 0 {
		a.note(c, "暂无执行用量。")
	}
}
func (a *preview) liveComputerSettings(c *ui.Context) {
	if a.resourceFeedback(c, "computer", a.loadSettingsPage) {
		return
	}
	l := a.Live
	m := object(l.Data["computer"])
	if l.Data["computer"] == nil {
		a.note(c, "正在读取电脑控制状态，请连接主机后刷新。")
		return
	}
	a.note(c, "开关对新会话生效。实际权限由当前运行主机的系统管理。")
	enabled := boolean(m["enabled"])
	a.preferenceRow(c, "启用电脑控制", "允许新会话使用电脑控制工具。", func() {
		if ui.Switch(c, &enabled).Label("启用电脑控制").Disabled(l.Loading["computer-save"]).Changed() {
			a.operation("computer-save", "POST", "/computer/settings", map[string]any{"enabled": enabled}, func(v any) { l.Data["computer"] = v })
		}
	})
	a.preferenceRow(c, "审批策略", "普通操作可自动执行；高风险操作仍会询问。", func() {
		ui.Row(c).Gap(8).Children(func() {
			for _, choice := range []struct{ key, label string }{{"ask", "逐批询问"}, {"auto", "自动执行"}} {
				selectedButton(c, a.action(c, choice.label), str(m["approval_policy"]) == choice.key).Disabled(l.Loading["computer-save"]).OnClick(func() {
					a.operation("computer-save", "POST", "/computer/settings", map[string]any{"approval_policy": choice.key}, func(v any) { l.Data["computer"] = v })
				})
			}
		})
	})
	availability, permissions, lease := "不可用（需要 macOS 14+ 桌面组件）", "当前主机未提供电脑控制组件", str(m["lease_holder"])
	if lease == "" {
		lease = "空闲"
	}
	if boolean(m["available"]) {
		availability = "组件已就绪，等待系统授权"
		permissions = "缺少系统授权"
		if missing := list(m["missing"]); len(missing) > 0 {
			permissions = "缺少：" + displayText(missing)
		}
	}
	if boolean(m["granted"]) {
		availability = "就绪"
		permissions = "已授予"
	}
	for _, row := range []struct{ label, value string }{{"Provider", str(m["provider"])}, {"可用性", availability}, {"系统权限", permissions}, {"当前占用", lease}, {"已控制应用（审计）", displayText(m["approved_apps"])}} {
		a.preferenceRow(c, row.label, row.value, func() {})
	}
	a.action(c, "请求系统权限").AlignSelf(ui.Start).Disabled(!a.Settings.host().Local || !boolean(m["available"]) || l.Loading["permissions"]).OnClick(func() {
		a.operation("permissions", "POST", "/computer/permissions", map[string]any{}, func(any) { a.loadSettingsPage() })
	})
	a.danger(c, "清除应用审计记录").AlignSelf(ui.Start).OnClick(func() {
		l.Confirm = &nativeConfirmation{"清除应用审计记录？", "仅清除审批记录，不会撤销系统权限。", func() {
			a.operation("computer-save", "POST", "/computer/settings", map[string]any{"clear_approved_apps": true}, func(v any) { l.Data["computer"] = v })
		}}
	})
}
func (a *preview) liveCapabilitySettings(c *ui.Context) {
	s := &a.Settings
	if s.CapabilityTab == "" {
		s.CapabilityTab = "mine"
	}
	ui.Row(c).Gap(10).Wrap().AlignItems(ui.Center).Children(func() {
		ui.Row(c).Gap(2).Padding(3).Radius(8).Background(c.Theme().Surface).Border(1, c.Theme().Border).Children(func() {
			tabs := []struct{ id, label string }{
				{"mine", "我的技能"},
				{"market", "市场"},
			}
			for _, t := range tabs {
				selected := s.CapabilityTab == t.id
				btn := flatButton(c, "", a.tr(t.label)).Padding(7, 12).Selected(selected).OnClick(func() {
					if s.CapabilityTab != t.id {
						a.Settings.Notice, a.Settings.Error = "", ""
					}
					s.CapabilityTab = t.id
					if t.id == "market" {
						a.openSkillMarket()
					} else {
						a.loadLocalSkills()
					}
				})
				if selected {
					btn.TextColor(c.Theme().Text).Background(c.Theme().SurfaceHover)
				}
			}
		})
		ui.Spacer(c)
		a.action(c, "刷新").Disabled(a.Live.Loading[localSkillsKey] || a.Live.Loading["market"]).OnClick(func() {
			if s.CapabilityTab == "market" {
				a.searchMarket()
			} else {
				a.refreshSkillLists()
			}
		})
	})
	if s.CapabilityTab == "market" {
		a.liveMarketSettings(c)
		return
	}
	a.liveLocalSkills(c)
}
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}
func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
func idString(v any) string {
	if s := str(v); s != "" {
		return s
	}
	return strconv.FormatInt(int64(number(v)), 10)
}
func pathSegment(v string) string { return url.PathEscape(v) }

func thinkingMode(label string) string {
	switch label {
	case "沿用模型默认":
		return ""
	case "开启思考":
		return "enabled"
	case "关闭思考":
		return "disabled"
	case "自适应思考":
		return "adaptive"
	}
	return label
}
func thinkingLabel(mode string) string {
	switch mode {
	case "":
		return "沿用模型默认"
	case "enabled":
		return "开启思考"
	case "disabled":
		return "关闭思考"
	case "adaptive":
		return "自适应思考"
	}
	return mode
}
func reasoningEffort(label string) string {
	if label == "沿用模型默认" {
		return ""
	}
	return label
}
