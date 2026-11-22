package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// ConfigSnapshot intentionally contains no credentials or secret-bearing URLs.
type ConfigSnapshot struct{ Provider, Model, Source, DataDir, DefaultPath, Warning string }

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
	m.configEditing = false
	m.configIndex = 0
	m.configNotice = ""
	return m, nil
}

func (m *TuiModel) handleConfigKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		m.configIndex = (m.configIndex + 3) % 4
	case tea.KeyDown:
		m.configIndex = (m.configIndex + 1) % 4
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

func (m *TuiModel) renderConfig() string {
	snapshot := m.configBackend.Snapshot()
	lines := []string{
		m.theme.StatusAccent.Render("设置 · 独立 CLI"),
		"",
		fmt.Sprintf("当前会话: %s/%s", m.provider, m.modelID),
		fmt.Sprintf("当前默认: %s/%s", snapshot.Provider, snapshot.Model),
		"配置来源: " + snapshot.Source,
		"默认作用于新的 CLI 会话，不修改桌面主机",
		"默认保存: " + snapshot.DefaultPath,
		"会话数据: " + snapshot.DataDir,
	}
	if snapshot.Warning != "" {
		lines = append(lines, "注意: "+snapshot.Warning)
	}
	lines = append(lines, "")
	items := []string{"从列表设置默认模型", "手动输入默认模型 ID", "切换当前会话模型", "返回对话"}
	for i, item := range items {
		prefix := "  "
		if i == m.configIndex {
			prefix = "› "
		}
		lines = append(lines, prefix+item)
	}
	if m.configEditing {
		lines = append(lines, "", "模型 ID: "+m.configInput+"▏", "Enter 保存 · Esc 取消（手动输入不会验证模型可用性）")
	}
	if m.configNotice != "" {
		lines = append(lines, "", m.configNotice)
	}
	lines = append(lines, "", "↑↓ 选择 · Enter 确认 · Esc 返回")
	height := max(1, m.height)
	if len(lines) > height { // Keep actions and feedback visible in small terminals.
		start := max(0, len(lines)-height)
		lines = lines[start:]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	width := max(1, m.width)
	for i, line := range lines {
		lines[i] = ansi.Truncate(terminalText(line), width, "")
	}
	tea.SetCursorPosition(0, 0)
	return strings.Join(lines, "\n")
}
