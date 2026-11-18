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
