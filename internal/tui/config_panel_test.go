package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

type fakeConfigBackend struct {
	snapshot ConfigSnapshot
	calls    int
}

func (f *fakeConfigBackend) Snapshot() ConfigSnapshot { return f.snapshot }
func (f *fakeConfigBackend) SetDefault(_ context.Context, p, m string) error {
	f.calls++
	f.snapshot.Provider = p
	f.snapshot.Model = m
	return nil
}

func TestConfigPanelDefaultDoesNotSwitchCurrentSession(t *testing.T) {
	m := newSelectorTestModel(t)
	backend := &fakeConfigBackend{snapshot: ConfigSnapshot{Provider: "openai", Model: "model-00", DefaultPath: "/safe/cli-default-model.json", DataDir: "/safe/data"}}
	m.configBackend = backend
	m.handleSlashCommand("/config")
	require.True(t, m.configOpen)
	require.Contains(t, m.View(), "默认")
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, m.configModelPicker)
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, m.configOpen)
	require.False(t, m.modelSelect)
	require.Equal(t, "model-01", backend.snapshot.Model)
	_, current := m.session.ModelInfo()
	require.Equal(t, "model-00", current)
	require.Contains(t, m.View(), "已保存")
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	require.False(t, m.configOpen)
}

func TestConfigPanelCustomModelAndSmallScreen(t *testing.T) {
	m := newSelectorTestModel(t)
	backend := &fakeConfigBackend{snapshot: ConfigSnapshot{Provider: "openai", Model: "model-00", DefaultPath: "/config", DataDir: "/data"}}
	m.configBackend = backend
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m.handleSlashCommand("/config")
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, m.configEditing)
	for _, r := range "gateway/custom" {
		m.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	require.LessOrEqual(t, lipgloss.Height(m.View()), 12)
	require.LessOrEqual(t, lipgloss.Width(m.View()), 40)
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, "gateway/custom", backend.snapshot.Model)
	require.False(t, m.configEditing)
	require.True(t, strings.Contains(m.configNotice, "已保存"))
}

func TestConfigPanelSelectionRemainsVisibleOnSmallScreens(t *testing.T) {
	m := newSelectorTestModel(t)
	m.configBackend = &fakeConfigBackend{snapshot: ConfigSnapshot{Provider: "openai", Model: "test"}}
	m.handleSlashCommand("/config")
	items := []string{"从列表设置默认模型", "手动输入默认模型 ID", "切换当前会话模型", "飞书看门狗", "返回对话"}
	for _, height := range []int{2, 4, 8, 12, 30} {
		m.Update(tea.WindowSizeMsg{Width: 40, Height: height})
		for i, item := range items {
			m.configIndex = i
			view := m.View()
			require.Contains(t, view, item)
			require.LessOrEqual(t, lipgloss.Width(view), 40)
			require.LessOrEqual(t, lipgloss.Height(view), height)
		}
	}
}
