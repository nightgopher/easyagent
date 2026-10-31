package agentdata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func memoryAgent(t *testing.T, s *Store) Agent {
	t.Helper()
	a, err := s.SaveAgent(context.Background(), Agent{Name: "Role", Instructions: "Role", MemoryEnabled: true})
	require.NoError(t, err)
	return a
}

func TestMemoryIsolationScopesExpiryAndCorrections(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	s := New(dir)
	a, b := memoryAgent(t, s), memoryAgent(t, s)
	workspace := t.TempDir()
	m, err := s.SaveMemory(ctx, a.ID, workspace, "user", "", Memory{Key: "沟通偏好", Content: "请通过邮件联系"})
	require.NoError(t, err)
	_, err = s.SaveMemory(ctx, a.ID, workspace, "agent", "session-a", Memory{Key: "项目事实", Content: "合同已经确认", Scope: "workspace"})
	require.NoError(t, err)
	_, err = s.SaveMemory(ctx, a.ID, workspace, "user", "", Memory{Key: "临时偏好", Content: "周五联系", ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	require.NoError(t, err)
	all, err := New(dir).SearchMemories(ctx, a.ID, workspace, "")
	require.NoError(t, err)
	require.Len(t, all, 2)
	all, err = s.SearchMemories(ctx, a.ID, t.TempDir(), "")
	require.NoError(t, err)
	require.Len(t, all, 1)
	all, err = s.SearchMemories(ctx, b.ID, workspace, "")
	require.NoError(t, err)
	require.Empty(t, all)
	_, err = s.GetMemory(ctx, b.ID, m.ID)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, s.DeleteMemory(ctx, b.ID, m.ID, m.Version), ErrConflict)
	for _, query := range []string{"邮件联系", "邮件", "邮"} {
		all, err = s.SearchMemories(ctx, a.ID, workspace, query)
		require.NoError(t, err)
		require.Len(t, all, 1)
	}
	old := m
	m.Content = "改用电话联系"
	m, err = s.SaveMemory(ctx, a.ID, workspace, "user", "", m)
	require.NoError(t, err)
	require.Equal(t, old.CreatedAt, m.CreatedAt)
	_, err = s.SaveMemory(ctx, a.ID, workspace, "user", "", old)
	require.ErrorIs(t, err, ErrConflict)
	all, err = s.SearchMemories(ctx, a.ID, workspace, "邮件联系")
	require.NoError(t, err)
	require.Empty(t, all)
	all, err = s.SearchMemories(ctx, a.ID, workspace, "电话联系")
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.NoError(t, s.DeleteMemory(ctx, a.ID, m.ID, m.Version))
	all, err = s.SearchMemories(ctx, a.ID, workspace, "电话联系")
	require.NoError(t, err)
	require.Empty(t, all)
	_, err = s.GetMemory(ctx, a.ID, m.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMemoryDedupAndTrustedOwnership(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	a := memoryAgent(t, s)
	input := Memory{AgentID: "forged", SourceKind: "user", SourceSession: "forged-session", Key: "preference", Content: "Use English"}
	m, err := s.SaveMemory(ctx, a.ID, t.TempDir(), "agent", "real-session", input)
	require.NoError(t, err)
	require.Equal(t, a.ID, m.AgentID)
	require.Equal(t, "agent", m.SourceKind)
	require.Equal(t, "real-session", m.SourceSession)
	duplicate, err := s.SaveMemory(ctx, a.ID, t.TempDir(), "agent", "real-session", input)
	require.NoError(t, err)
	require.Equal(t, m.ID, duplicate.ID)
	input.Content = "Use Chinese"
	_, err = s.SaveMemory(ctx, a.ID, t.TempDir(), "agent", "real-session", input)
	require.ErrorIs(t, err, ErrConflict)
	input.ExpiresAt = "tomorrow"
	_, err = s.SaveMemory(ctx, a.ID, t.TempDir(), "agent", "real-session", input)
	require.Error(t, err)
	a.MemoryEnabled = false
	_, err = s.SaveAgent(ctx, a)
	require.NoError(t, err)
	_, err = s.SearchMemories(ctx, a.ID, "", "")
	require.Error(t, err)
	items, more, err := s.ListMemories(ctx, a.ID, "", 0)
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, items, 1, "disabling memory preserves records for owner management")
}

func TestMemoryManagementPaginationAndExpiryNormalization(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	a := memoryAgent(t, s)
	for i := 0; i < 53; i++ {
		_, err := s.SaveMemory(ctx, a.ID, "", "user", "", Memory{Key: fmt.Sprintf("item-%02d", i), Content: "common"})
		require.NoError(t, err)
	}
	first, more, err := s.ListMemories(ctx, a.ID, "", 0)
	require.NoError(t, err)
	require.Len(t, first, 50)
	require.True(t, more)
	last, more, err := s.ListMemories(ctx, a.ID, "", 50)
	require.NoError(t, err)
	require.Len(t, last, 3)
	require.False(t, more)
	for _, x := range last {
		for _, y := range first {
			require.NotEqual(t, x.ID, y.ID)
		}
	}
	found, _, err := s.ListMemories(ctx, a.ID, "item-02", 0)
	require.NoError(t, err)
	require.Len(t, found, 1)
	input := Memory{Key: "expiry", Content: "same", ExpiresAt: "2030-01-01T08:00:00+08:00"}
	one, err := s.SaveMemory(ctx, a.ID, "", "user", "", input)
	require.NoError(t, err)
	two, err := s.SaveMemory(ctx, a.ID, "", "user", "", input)
	require.NoError(t, err)
	require.Equal(t, one.ID, two.ID)
}
