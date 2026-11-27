package app

import (
	"path/filepath"
	"testing"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

func TestProjectScopeRejectsRestoringForeignSessions(t *testing.T) {
	a := guardProfileApp(t, func(cfg *config.Config) { cfg.ContextScope = "project" })
	current, err := a.NewSession(t.Context())
	require.NoError(t, err)
	require.NoError(t, a.SessionManager().SaveMeta(current.SessionID(), t.TempDir(), "coding"))
	_, err = a.LoadSession(t.Context(), current.SessionID())
	require.ErrorContains(t, err, "workspace does not match")
}

func TestProjectScopeRejectsAlternateAgentSetup(t *testing.T) {
	a := guardProfileApp(t, func(cfg *config.Config) { cfg.ContextScope = "project" })
	for _, tc := range []struct{ application, agentID string }{
		{"business", "agent-other"}, {"kb", ""},
	} {
		_, _, err := a.SessionSetup(t.Context(), tc.application, tc.agentID, a.Config())
		require.Error(t, err)
	}
	cfg := a.Config()
	cfg.Workspace = filepath.Dir(cfg.Workspace)
	_, _, err := a.SessionSetup(t.Context(), "coding", "", cfg)
	require.ErrorContains(t, err, "workspace does not match")
}
