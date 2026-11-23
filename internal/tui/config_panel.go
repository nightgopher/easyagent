package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// ConfigSnapshot intentionally contains no credentials or secret-bearing URLs.
type ConfigSnapshot struct{ Provider, Model, Source, DataDir, DefaultPath, Warning string }

type WatchSnapshot struct{ CoreURL, Session, Interval, Status, ConfigPath, Warning string }
type WatchBackend interface {
	WatchSnapshot(context.Context) WatchSnapshot
	SetWatchField(string, string) error
	SetWatchRunning(context.Context, bool) error
}

// ConfigBackend exposes the independent CLI's model defaults to its TUI.
type ConfigBackend interface {
	Snapshot() ConfigSnapshot
	SetDefault(context.Context, string, string) error
}

func (m *TuiModel) openConfig() (tea.Model, tea.Cmd) {
	if m.agentBusy || m.slashCmdBusy {
		m.copyNotice = "任务运行中，完成后再打开设置"
		return m, nil
	}
	if m.configBackend == nil {
		m.messages = append(m.messages, ChatMessage{Role: "system", Content: "此入口不支持设置面板。"})
		m.viewport.SetMessages(m.messages)
		return m, nil
	}
	m.completion.Close()
	m.configOpen = true
	m.configWatch = false
	m.configEditing = false
	m.configIndex = 0
	m.configNotice = ""
	return m, nil
}

func (m *TuiModel) handleConfigKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.configWatch {
		return m.handleWatchKey(msg)
	}
	if m.configEditing {
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.configEditing = false
			m.configInput = ""
			m.configNotice = ""
		case tea.KeyEnter:
			model := strings.TrimSpace(m.configInput)
			if model == "" {
				m.configNotice = "请输入模型 ID"
				return m, nil
			}
			provider := m.configBackend.Snapshot().Provider
			m.saveConfigModel(provider, model)
			if !strings.HasPrefix(m.configNotice, "保存失败") {
				m.configEditing = false
				m.configInput = ""
			}
		case tea.KeyBackspace, tea.KeyCtrlH:
			r := []rune(m.configInput)
			if len(r) > 0 {
				m.configInput = string(r[:len(r)-1])
			}
		case tea.KeyCtrlU:
			m.configInput = ""
		case tea.KeySpace:
			if len(m.configInput) < 200 {
				m.configInput += " "
			}
		case tea.KeyRunes:
			if utf8.RuneCountInString(m.configInput)+len(msg.Runes) <= 200 {
				m.configInput += strings.ReplaceAll(sanitizeInput(string(msg.Runes)), "\n", "")
			}
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.configOpen = false
		m.configNotice = ""
	case tea.KeyUp:
		m.configIndex = (m.configIndex + 4) % 5
	case tea.KeyDown:
		m.configIndex = (m.configIndex + 1) % 5
	case tea.KeyEnter:
		switch m.configIndex {
		case 0:
			provider := m.configBackend.Snapshot().Provider
			var opts []ModelOption
			for _, item := range m.getAvailableModels() {
				if item.Provider == provider {
					opts = append(opts, item)
				}
			}
			if !m.completion.TriggerModel(opts) {
				m.configNotice = "没有可选模型；可手动输入模型 ID"
				return m, nil
			}
			m.modelCatalog = append([]CompletionItem(nil), m.completion.items...)
			m.modelSelect = true
			m.configModelPicker = true
		case 1:
			m.configEditing = true
			m.configInput = ""
			m.configNotice = ""
		case 2:
			_, cmd := m.openModelSelector()
			if !m.modelSelect {
				m.configNotice = "模型目录为空"
			}
			return m, cmd
		case 3:
			if _, ok := m.configBackend.(WatchBackend); !ok {
				m.configNotice = "当前入口不支持看门狗"
				return m, nil
			}
			m.configWatch = true
			m.configIndex = 0
			m.configNotice = ""
			m.refreshWatch()
		case 4:
			m.configOpen = false
		}
	}
	return m, nil
}

func (m *TuiModel) saveConfigModel(provider, model string) {
	if err := m.configBackend.SetDefault(context.Background(), provider, model); err != nil {
		m.configNotice = "保存失败: " + err.Error()
		return
	}
	m.configNotice = "已保存 CLI 默认模型，新会话使用；现有会话保持不变"
}

// configScreen keeps navigation visible even when the terminal is short.
// Dynamic text is sanitized before styling; styling itself must not be stripped.
func (m *TuiModel) configScreen(title string, summary, items []string, editing, notice, help string) string {
	width, height := max(1, m.width), max(1, m.height)
	clean := func(s string) string { return strings.ReplaceAll(terminalText(s), "\n", " ") }
	var top []string
	top = append(top, m.theme.StatusAccent.Render("  ◇  "+clean(title)))
	if height >= 10 {
		top = append(top, m.theme.Separator.Render("  "+strings.Repeat("─", max(0, min(width-2, 60)))))
		// Prefer room for actions over less essential details on short screens.
		limit := max(0, height-len(items)-len(top)-5)
		for _, line := range summary {
			if limit == 0 {
				break
			}
			top = append(top, "  "+m.theme.StatusDim.Render(clean(line)))
			limit--
		}
		if limit > 0 {
			top = append(top, "")
		}
	}
	var bottom []string
	if editing != "" {
		bottom = append(bottom, m.theme.StatusAccent.Render("  "+clean(editing)))
	}
	if notice != "" {
		bottom = append(bottom, m.theme.WarnText.Render("  "+clean(notice)))
	}
	if height > 2 {
		bottom = append(bottom, m.theme.HelpText.Render("  "+clean(help)))
	}
	// Always reserve a row for the selected action on very small screens.
	if len(bottom) > height-1 {
		bottom = bottom[:height-1]
	}
	if len(top) > height-len(bottom)-1 {
		top = top[:height-len(bottom)-1]
	}
	available := max(1, height-len(top)-len(bottom))
	count := min(len(items), available)
	start := max(0, min(m.configIndex-count+1, len(items)-count))
	lines := append([]string{}, top...)
	for i := start; i < start+count; i++ {
		line := "    " + clean(items[i])
		if i == m.configIndex {
			line = m.theme.StatusAccent.Render("  › " + clean(items[i]))
		}
		lines = append(lines, line)
	}
	lines = append(lines, bottom...)
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	return strings.Join(lines, "\n")
}

func (m *TuiModel) renderConfig() string {
	if m.configWatch {
		return m.renderWatch()
	}
	s := m.configBackend.Snapshot()
	summary := []string{
		fmt.Sprintf("当前会话  %s/%s", m.provider, m.modelID),
		fmt.Sprintf("默认模型  %s/%s", s.Provider, s.Model),
		"配置来源  " + s.Source,
		"默认仅用于新的 CLI 会话，不修改桌面主机",
		"默认保存  " + s.DefaultPath,
		"会话数据  " + s.DataDir,
	}
	if s.Warning != "" {
		summary = append([]string{"注意  " + s.Warning}, summary...)
	}
	items := []string{"从列表设置默认模型", "手动输入默认模型 ID", "切换当前会话模型", "飞书看门狗", "返回对话"}
	editing := ""
	help := "↑↓ 选择  ·  Enter 确认  ·  Esc 返回"
	if m.configEditing {
		editing = "模型 ID  " + m.configInput + "▏"
		help = "Enter 保存  ·  Ctrl+U 清空  ·  Esc 取消"
	}
	return m.configScreen("设置  /  独立 CLI", summary, items, editing, m.configNotice, help)
}

func (m *TuiModel) refreshWatch() {
	b := m.configBackend.(WatchBackend)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m.watchState = b.WatchSnapshot(ctx)
}
func (m *TuiModel) openWatch() (tea.Model, tea.Cmd) {
	_, cmd := m.openConfig()
	if !m.configOpen {
		return m, cmd
	}
	if _, ok := m.configBackend.(WatchBackend); !ok {
		m.configNotice = "当前入口不支持看门狗"
		return m, cmd
	}
	m.configWatch = true
	m.configIndex = 0
	m.refreshWatch()
	return m, cmd
}
func (m *TuiModel) handleWatchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	b := m.configBackend.(WatchBackend)
	if m.configEditing {
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.configEditing = false
			m.configNotice = ""
		case tea.KeyEnter:
			fields := []string{"url", "session", "interval"}
			err := b.SetWatchField(fields[m.configIndex], m.configInput)
			if err != nil {
				m.configNotice = "保存失败: " + err.Error()
			} else {
				m.configEditing = false
				m.configNotice = "已保存；运行中的服务需重启才能应用"
				m.refreshWatch()
			}
		case tea.KeyCtrlU:
			m.configInput = ""
		case tea.KeyBackspace, tea.KeyCtrlH:
			r := []rune(m.configInput)
			if len(r) > 0 {
				m.configInput = string(r[:len(r)-1])
			}
		case tea.KeySpace:
			if len(m.configInput) < 500 {
				m.configInput += " "
			}
		case tea.KeyRunes:
			if utf8.RuneCountInString(m.configInput)+len(msg.Runes) <= 500 {
				m.configInput += strings.ReplaceAll(sanitizeInput(string(msg.Runes)), "\n", "")
			}
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.configWatch = false
		m.configIndex = 3
		m.configNotice = ""
	case tea.KeyUp:
		m.configIndex = (m.configIndex + 5) % 6
	case tea.KeyDown:
		m.configIndex = (m.configIndex + 1) % 6
	case tea.KeyEnter:
		switch m.configIndex {
		case 0, 1, 2:
			m.configEditing = true
			m.configNotice = ""
			m.configInput = []string{m.watchState.CoreURL, m.watchState.Session, m.watchState.Interval}[m.configIndex]
			if m.configIndex == 1 && m.configInput == "自动发现" {
				m.configInput = ""
			}
		case 3, 4:
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := b.SetWatchRunning(ctx, m.configIndex == 3)
			cancel()
			if err != nil {
				m.configNotice = "操作失败: " + err.Error()
			} else if m.configIndex == 3 {
				m.configNotice = "启动请求已提交；SSH 断开后运行依赖 user service 和 linger"
			} else {
				m.configNotice = "已停止并取消开机启动"
			}
			m.refreshWatch()
		case 5:
			m.configWatch = false
			m.configIndex = 3
			m.configNotice = ""
		}
	}
	return m, nil
}
func (m *TuiModel) renderWatch() string {
	s := m.watchState
	summary := []string{"状态  " + s.Status, "配置  " + s.ConfigPath, "需要独立核心服务与已登录的 lark-cli", "SSH 断开后持续运行需要 systemd linger", "认证令牌配置在 cli-watch.env"}
	if s.Warning != "" {
		summary = append([]string{"注意  " + s.Warning}, summary...)
	}
	items := []string{"核心地址  " + s.CoreURL, "会话 ID  " + s.Session, "轮询间隔  " + s.Interval, "启动并启用用户服务", "停止并禁用用户服务", "返回设置"}
	editing := ""
	help := "↑↓ 选择  ·  Enter 确认  ·  Esc 返回"
	if m.configEditing {
		editing = "输入  " + m.configInput + "▏"
		help = "Enter 保存  ·  Ctrl+U 清空  ·  Esc 取消"
	}
	return m.configScreen("设置  /  飞书看门狗", summary, items, editing, m.configNotice, help)
}
