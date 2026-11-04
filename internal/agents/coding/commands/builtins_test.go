package commands

import (
	"context"
	"testing"

	"github.com/nightgopher/easyagent/sdk/slashcmd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSession struct {
	confirmEnabled bool
	sessionID      string
	provider       string
	modelID        string
	toolNames      []string
	switchErr      error
	profile        string
	goal           string
	compactResult  string
	compactFrom    int
	compactTo      int
	compactErr     error
}

func (m *mockSession) SessionID() string { return m.sessionID }

func (m *mockSession) ModelInfo() (string, string) {
	if m.provider == "" && m.modelID == "" {
		return "openai", "gpt-4o"
	}
	return m.provider, m.modelID
}

func (m *mockSession) SwitchModel(ctx context.Context, modelID string, provider string) error {
	if m.switchErr != nil {
		return m.switchErr
	}
	m.modelID = modelID
	if provider != "" {
		m.provider = provider
	}
	return nil
}

func (m *mockSession) ToolNames() []string {
	if m.toolNames == nil {
		return []string{"bash", "read", "write", "edit"}
	}
	return m.toolNames
}

func (m *mockSession) Profile() string {
	if m.profile == "" {
		return "coding"
	}
	return m.profile
}

func (m *mockSession) SwitchProfile(_ context.Context, profile string) error {
	switch profile {
	case "coding", "review":
		m.profile = profile
		return nil
	default:
		return assert.AnError
	}
}

func (m *mockSession) Goal() string                   { return m.goal }
func (m *mockSession) SetGoal(goal string)            { m.goal = goal }
func (m *mockSession) ClearGoal()                     { m.goal = "" }
func (m *mockSession) ConfirmEnabled() bool           { return m.confirmEnabled }
func (m *mockSession) SetConfirmEnabled(enabled bool) { m.confirmEnabled = enabled }
func (m *mockSession) Compact(_ context.Context, _ string) (string, int, int, error) {
	if m.compactErr != nil {
		return "", 0, 0, m.compactErr
	}
	return m.compactResult, m.compactFrom, m.compactTo, nil
}

type mockApp struct {
	sessions     []slashcmd.SessionInfo
	newSession   slashcmd.SessionContext
	switchTarget slashcmd.SessionContext
	newErr       error
	switchErr    error
}

func (m *mockApp) ListSessionsInfo() ([]slashcmd.SessionInfo, error) {
	return m.sessions, nil
}

func (m *mockApp) CreateSession(ctx context.Context) (slashcmd.SessionContext, error) {
	if m.newErr != nil {
		return nil, m.newErr
	}
	return m.newSession, nil
}

func (m *mockApp) SwitchSession(ctx context.Context, sessionID string) (slashcmd.SessionContext, error) {
	if m.switchErr != nil {
		return nil, m.switchErr
	}
	return m.switchTarget, nil
}

func (m *mockApp) Profiles() []string {
	return []string{"coding", "review"}
}

func (m *mockApp) AvailableModels() []slashcmd.ModelInfo {
	return []slashcmd.ModelInfo{
		{Provider: "anthropic", ModelID: "claude-sonnet-4-6"},
		{Provider: "openai", ModelID: "gpt-4o"},
	}
}

func newRegistry() *slashcmd.Registry {
	reg := slashcmd.NewRegistry()
	RegisterBuiltins(reg)
	return reg
}

func TestConfirmCommand_TogglesAndStatus(t *testing.T) {
	reg := newRegistry()
	sess := &mockSession{sessionID: "test", confirmEnabled: true}
	cmdCtx := slashcmd.Context{
		Ctx:     context.Background(),
		Session: sess,
		App:     &mockApp{},
	}

	// 状态查询
	result, err := reg.Execute(cmdCtx, "/confirm")
	require.NoError(t, err)
	assert.Contains(t, result.Output, "确认已开启")

	// off → 全权
	result, err = reg.Execute(cmdCtx, "/confirm off")
	require.NoError(t, err)
	assert.Contains(t, result.Output, "全权模式")
	assert.False(t, sess.ConfirmEnabled())

	// 全权下状态查询
	result, err = reg.Execute(cmdCtx, "/confirm")
	require.NoError(t, err)
	assert.Contains(t, result.Output, "全权模式")

	// on → 恢复
	result, err = reg.Execute(cmdCtx, "/confirm on")
	require.NoError(t, err)
	assert.Contains(t, result.Output, "已恢复确认")
	assert.True(t, sess.ConfirmEnabled())

	// 非法参数
	result, err = reg.Execute(cmdCtx, "/confirm blah")
	require.NoError(t, err)
	assert.Contains(t, result.Output, "用法")
}
