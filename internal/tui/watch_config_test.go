package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

type fakeWatchBackend struct {
	fakeConfigBackend
	watch   WatchSnapshot
	started bool
}

func (f *fakeWatchBackend) WatchSnapshot(context.Context) WatchSnapshot { return f.watch }
func (f *fakeWatchBackend) SetWatchField(field, value string) error {
	if field == "interval" {
		f.watch.Interval = value
	}
	return nil
}
func (f *fakeWatchBackend) SetWatchRunning(_ context.Context, on bool) error {
	f.started = on
	return nil
}
func TestWatchSlashConfigAndStart(t *testing.T) {
	m := newSelectorTestModel(t)
	b := &fakeWatchBackend{watch: WatchSnapshot{CoreURL: "http://localhost:8080", Interval: "30s", Status: "未启动"}}
	m.configBackend = b
	m.handleSlashCommand("/watch")
	require.True(t, m.configWatch)
	require.Contains(t, m.View(), "看门狗")
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlU})
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("45s")})
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, "45s", b.watch.Interval)
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, b.started)
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	m.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.False(t, b.started)
}
