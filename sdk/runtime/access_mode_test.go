package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/extensions"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
	"github.com/stretchr/testify/require"
)

func TestPersistedAccessModeAndLegacyDefault(t *testing.T) {
	ctx := context.Background()
	manager := sessionmgr.NewManager(t.TempDir())
	cfg := config.Default()
	deps := Dependencies{SessionMgr: manager, Registry: providers.NewRegistry(), ExtRegistry: extensions.NewRegistry(), Application: &confirmationTestApplication{}}
	confirm := func(context.Context, agent.ConfirmationRequest) agent.ConfirmDecision { return agent.ConfirmDecision{} }
	s, err := NewAgentSession(ctx, AgentSessionOptions{Config: cfg}, deps)
	require.NoError(t, err)
	id := s.SessionID()
	require.NoError(t, s.TrySetInitialAccessMode(confirm, "full"))
	require.Equal(t, "ask", s.AccessMode(), "a new session already owns its chosen default")
	require.NoError(t, s.TrySetAccessMode(confirm, false))
	require.NoError(t, s.Close())
	s, err = NewAgentSession(ctx, AgentSessionOptions{Config: cfg, SessionID: id}, deps)
	require.NoError(t, err)
	defer s.Close()
	require.Equal(t, "full", s.AccessMode())
	require.NoError(t, s.TrySetInitialAccessMode(confirm, "ask"))
	require.Equal(t, "full", s.AccessMode(), "defaults cannot reset an explicit choice")
	meta := filepath.Join(manager.SessionsDir(), id, "meta.json")
	require.NoError(t, os.Rename(meta, meta+".backup"))
	require.NoError(t, os.Mkdir(meta, 0700))
	require.Error(t, s.TrySetAccessMode(confirm, true))
	require.Equal(t, "full", s.AccessMode(), "failed persistence restores the live mode")
	legacyID, _, err := manager.Create(ctx)
	require.NoError(t, err)
	legacy, err := NewAgentSession(ctx, AgentSessionOptions{Config: cfg, SessionID: legacyID}, deps)
	require.NoError(t, err)
	defer legacy.Close()
	require.NoError(t, legacy.TrySetInitialAccessMode(confirm, "full"))
	require.Equal(t, "full", legacy.AccessMode())
	require.NoError(t, legacy.TrySetInitialAccessMode(confirm, "ask"))
	require.Equal(t, "full", legacy.AccessMode(), "adopt the legacy default only once")
	saved, err := manager.LoadAccessMode(legacyID)
	require.NoError(t, err)
	require.Equal(t, "full", saved)
	require.NoError(t, os.WriteFile(filepath.Join(manager.SessionsDir(), legacyID, "meta.json"), []byte(`{"access_mode":"invalid"}`), 0600))
	_, err = NewAgentSession(ctx, AgentSessionOptions{Config: cfg, SessionID: legacyID}, deps)
	require.Error(t, err, "corrupt saved permissions must not silently adopt full access")
}
