package agentdata

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentPersistenceAndConcurrentEdits(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	s := New(dir)
	a, err := s.SaveAgent(ctx, Agent{Name: "客户助手", Instructions: "整理客户反馈"})
	require.NoError(t, err)
	require.NotEmpty(t, a.ID)
	require.Equal(t, DefaultTools(), a.Tools)
	reloaded, err := New(dir).GetAgent(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, a, reloaded)
	a.Name = "客户跟进"
	updated, err := s.SaveAgent(ctx, a)
	require.NoError(t, err)
	require.Greater(t, updated.Version, a.Version)
	_, err = s.SaveAgent(ctx, reloaded)
	require.ErrorIs(t, err, ErrConflict)
	all, err := s.ListAgents(ctx)
	require.NoError(t, err)
	require.Equal(t, []Agent{updated}, all)
	_, err = s.GetAgent(ctx, "../../missing")
	require.ErrorIs(t, err, ErrNotFound)
	info, err := os.Stat(filepath.Join(dir, "agents", "agents.sqlite"))
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0077)
}

func TestAgentValidationAndEmptyPermissions(t *testing.T) {
	s := New(t.TempDir())
	for _, a := range []Agent{
		{Name: "", Instructions: "role"}, {Name: "name", Instructions: ""},
		{Name: "name", Instructions: "role", Workspace: "relative"},
		{Name: "name", Instructions: "role", Tools: []string{"read", "read"}},
	} {
		_, err := s.SaveAgent(context.Background(), a)
		require.Error(t, err)
	}
	a, err := s.SaveAgent(context.Background(), Agent{Name: "No tools", Instructions: "Discuss", Tools: []string{}})
	require.NoError(t, err)
	require.Empty(t, a.Tools)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.GetAgent(ctx, a.ID)
	require.ErrorIs(t, err, context.Canceled)
}
