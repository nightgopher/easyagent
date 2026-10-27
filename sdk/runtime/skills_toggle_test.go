package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/extensions"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
	"github.com/stretchr/testify/require"
)

func TestSkillToggleAppliesOnNewSessionAndRefreshWithoutDuplicateFallback(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir, cfg.Workspace, cfg.Provider = t.TempDir(), t.TempDir(), "openai"
	cfg.SkillMarketEnabled = false
	first, second := t.TempDir(), t.TempDir()
	for _, root := range []string{first, second} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(`---
name: shared
description: test skill
---
Test`), 0600))
	}
	path := filepath.Join(first, "SKILL.md")
	newSession := func() *AgentSession {
		s, err := NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg, SkillDirs: []string{first, second}}, Dependencies{
			SessionMgr: sessionmgr.NewManager(cfg.DataDir), Registry: providers.NewRegistry(),
			ExtRegistry: extensions.NewRegistry(), Application: &confirmationTestApplication{},
		})
		require.NoError(t, err)
		t.Cleanup(func() { s.Close() })
		return s
	}
	existing := newSession()
	require.Len(t, existing.LoadedSkills(), 1)
	require.NoError(t, config.SetSkillEnabled(cfg.DataDir, path, false))
	require.Len(t, existing.LoadedSkills(), 1, "active context is retained until explicit refresh")
	require.Empty(t, newSession().LoadedSkills(), "disabled duplicate must not reveal another source")
	require.NoError(t, existing.RefreshTools(context.Background()))
	require.Empty(t, existing.LoadedSkills())
	require.NoError(t, config.SetSkillEnabled(cfg.DataDir, path, true))
	require.NoError(t, existing.RefreshTools(context.Background()))
	require.Len(t, existing.LoadedSkills(), 1)
	require.Equal(t, path, existing.LoadedSkills()[0].FilePath)
}
