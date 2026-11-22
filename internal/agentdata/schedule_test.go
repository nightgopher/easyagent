package agentdata

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScheduleNextFiring(t *testing.T) {
	at := time.Date(2026, 11, 15, 9, 30, 0, 0, time.Local)
	next, err := Schedule{DailyAt: "09:00"}.Next(at)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 16, 9, 0, 0, 0, time.Local), next)
	next, err = Schedule{DailyAt: "10:00"}.Next(at)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 15, 10, 0, 0, 0, time.Local), next)
	next, err = Schedule{Every: "2h"}.Next(at)
	require.NoError(t, err)
	require.Equal(t, at.Add(2*time.Hour), next)
	for _, bad := range []Schedule{{Every: "1m"}, {Every: "soon"}, {DailyAt: "25:00"}} {
		_, err = bad.Next(at)
		require.Error(t, err)
	}
}

func TestScheduleLifecycle(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	owner, err := s.SaveAgent(ctx, Agent{Name: "助理", Instructions: "整理待办"})
	require.NoError(t, err)
	for _, bad := range []Schedule{
		{Name: "x", Prompt: "p", Every: "1h"},                                 // no owner
		{Name: "x", Prompt: "p", AgentID: owner.ID, TeamID: "t", Every: "1h"}, // two owners
		{Name: "x", Prompt: "p", AgentID: owner.ID, Every: "1h", DailyAt: "09:00"},
		{Name: "x", Prompt: "p", AgentID: "missing", Every: "1h"},
		{Name: "", Prompt: "p", AgentID: owner.ID, Every: "1h"},
	} {
		_, err = s.SaveSchedule(ctx, bad)
		require.Error(t, err)
	}

	sc, err := s.SaveSchedule(ctx, Schedule{Name: " 每日巡检 ", Prompt: "检查 CI", AgentID: owner.ID, Every: "1h"})
	require.NoError(t, err)
	require.Equal(t, "每日巡检", sc.Name)
	require.EqualValues(t, 1, sc.Version)
	require.NotEmpty(t, sc.NextRunAt)

	due, err := s.DueSchedules(ctx, time.Now())
	require.NoError(t, err)
	require.Empty(t, due)
	due, err = s.DueSchedules(ctx, time.Now().Add(2*time.Hour))
	require.NoError(t, err)
	require.Len(t, due, 1)

	// Claiming moves the plan; a stale copy cannot claim it again after an edit.
	later := time.Now().Add(5 * time.Hour)
	claimed, err := s.AdvanceSchedule(ctx, due[0], later)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, s.RecordScheduleRun(ctx, sc.ID, ScheduleRun{StartedAt: "t", SessionID: "sess-1"}))
	got, err := s.GetSchedule(ctx, sc.ID)
	require.NoError(t, err)
	require.Equal(t, "sess-1", got.LastRun.SessionID)
	require.EqualValues(t, 1, got.Version, "runner writes never bump the user version")

	got.Disabled = true
	disabled, err := s.SaveSchedule(ctx, got)
	require.NoError(t, err)
	require.Empty(t, disabled.NextRunAt)
	require.Equal(t, "sess-1", disabled.LastRun.SessionID, "editing keeps the last run")
	claimed, err = s.AdvanceSchedule(ctx, due[0], later)
	require.NoError(t, err)
	require.False(t, claimed)
	due, err = s.DueSchedules(ctx, time.Now().Add(100*time.Hour))
	require.NoError(t, err)
	require.Empty(t, due)
	_, err = s.SaveSchedule(ctx, got)
	require.ErrorIs(t, err, ErrConflict)

	require.NoError(t, s.DeleteSchedule(ctx, sc.ID))
	require.ErrorIs(t, s.DeleteSchedule(ctx, sc.ID), ErrNotFound)
}

func TestScheduleFiringSurvivesRestartAndRejectsStaleClaim(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	store := New(dir)
	owner, err := store.SaveAgent(ctx, Agent{Name: "owner", Instructions: "work"})
	require.NoError(t, err)
	_, err = store.SaveSchedule(ctx, Schedule{Name: "job", Prompt: "run", AgentID: owner.ID, Every: "1h"})
	require.NoError(t, err)
	due, err := store.DueSchedules(ctx, time.Now().Add(2*time.Hour))
	require.NoError(t, err)
	require.Len(t, due, 1)
	next := time.Now().Add(4 * time.Hour)
	claimed, err := store.QueueScheduleFiring(ctx, due[0], next)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = store.QueueScheduleFiring(ctx, due[0], next)
	require.NoError(t, err)
	require.False(t, claimed)
	reopened := New(dir)
	pending, err := reopened.PendingScheduleFirings(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, reopened.SaveScheduleFiringSession(ctx, pending[0], "session-1"))
	require.ErrorIs(t, reopened.SaveScheduleFiringSession(ctx, pending[0], "session-2"), ErrConflict)
	reopened = New(dir)
	pending, err = reopened.PendingScheduleFirings(ctx)
	require.NoError(t, err)
	require.Equal(t, "session-1", pending[0].SessionID)
	require.NoError(t, reopened.FinishScheduleFiring(ctx, pending[0], ScheduleRun{SessionID: "session-1"}))
	pending, err = reopened.PendingScheduleFirings(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestEditingOrDisablingScheduleDoesNotReplayOldFiring(t *testing.T) {
	ctx := context.Background()
	store := New(t.TempDir())
	owner, err := store.SaveAgent(ctx, Agent{Name: "owner", Instructions: "work"})
	require.NoError(t, err)
	sc, err := store.SaveSchedule(ctx, Schedule{Name: "job", Prompt: "run", AgentID: owner.ID, Every: "1h"})
	require.NoError(t, err)
	due, err := store.DueSchedules(ctx, time.Now().Add(2*time.Hour))
	require.NoError(t, err)
	claimed, err := store.QueueScheduleFiring(ctx, due[0], time.Now().Add(4*time.Hour))
	require.NoError(t, err)
	require.True(t, claimed)
	sc.Disabled = true
	_, err = store.SaveSchedule(ctx, sc)
	require.NoError(t, err)
	pending, err := store.PendingScheduleFirings(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
}
