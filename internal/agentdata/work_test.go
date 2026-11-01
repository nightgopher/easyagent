package agentdata

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrackedWorkPersistsAndRejectsLateOrForeignReports(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	s := New(dir)
	w, err := s.TrackWork(ctx, WorkItem{SessionID: "owner", AgentID: "agent-a", Workspace: t.TempDir(), WorkUpdate: WorkUpdate{Title: "修复登录"}})
	require.NoError(t, err)
	retry, err := s.TrackWork(ctx, WorkItem{SessionID: "owner", WorkUpdate: WorkUpdate{Title: "must not overwrite"}})
	require.NoError(t, err)
	require.Equal(t, w, retry)
	v := w.WorkUpdate
	v.State, v.Summary = "waiting_review", "测试通过，等待用户验收"
	v.Artifacts = []WorkArtifact{{Title: "PR", Kind: "url", Value: "https://github.com/example/project/pull/1"}}
	_, err = s.UpdateWork(ctx, w.ID, "another-session", v)
	require.ErrorIs(t, err, ErrNotFound)
	updated, err := s.UpdateWork(ctx, w.ID, "owner", v)
	require.NoError(t, err)
	_, err = s.UpdateWork(ctx, w.ID, "owner", v)
	require.ErrorIs(t, err, ErrConflict)
	reloaded, err := New(dir).WorkForSession(ctx, "owner")
	require.NoError(t, err)
	require.Equal(t, updated, reloaded)
	v = updated.WorkUpdate
	v.State = "completed"
	_, err = s.UpdateWork(ctx, w.ID, "owner", v)
	require.Error(t, err)
	closed, err := s.UpdateWork(ctx, w.ID, "", v)
	require.NoError(t, err)
	require.True(t, closed.Closed())
	v = closed.WorkUpdate
	v.State = "active"
	_, err = s.UpdateWork(ctx, w.ID, "owner", v)
	require.Error(t, err)
	reopened, err := s.UpdateWork(ctx, w.ID, "", v)
	require.NoError(t, err)
	require.False(t, reopened.Closed())
	items, err := s.ListWork(ctx, "登录", "", 10, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	items, err = s.ListWork(ctx, "https://github.com", "", 10, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	items, err = s.ListWork(ctx, "", "another-session", 10, 0)
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestWorkInputValidation(t *testing.T) {
	for _, artifact := range []WorkArtifact{
		{Title: "x", Kind: "file", Value: "../secret"}, {Title: "x", Kind: "file", Value: "/etc/passwd"},
		{Title: "x", Kind: "url", Value: "javascript:alert(1)"}, {Title: "x", Kind: "url", Value: "https://user:secret@example.com"},
	} {
		_, err := New(t.TempDir()).TrackWork(context.Background(), WorkItem{SessionID: "s", WorkUpdate: WorkUpdate{Title: "x", Artifacts: []WorkArtifact{artifact}}})
		require.Error(t, err)
	}
	v := WorkUpdate{Title: "x", State: "waiting_answer"}
	require.Error(t, normalizeWork(&v))
	v.Question = "选哪个版本？"
	require.NoError(t, normalizeWork(&v))
	v.State = "active"
	require.NoError(t, normalizeWork(&v))
	require.Empty(t, v.Question)
}
