package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/stretchr/testify/require"
)

func TestScheduleFiresFreshTrackedSession(t *testing.T) {
	s, _, gateway := newRunTestServer(t)
	w := businessRequest(t, s, "POST", "/agents", map[string]any{"name": "助理", "instructions": "整理待办"}, 201)
	var owner agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &owner))
	businessRequest(t, s, "POST", "/schedules", map[string]any{"name": "巡检", "prompt": "检查 CI", "agent_id": owner.ID, "every": "1m"}, 400)
	w = businessRequest(t, s, "POST", "/schedules", map[string]any{"name": "巡检", "prompt": "检查 CI", "agent_id": owner.ID, "every": "1h"}, 201)
	var sc scheduleView
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &sc))

	// Nothing is due yet; an hour later the poller fires exactly once.
	s.fireDueSchedules(context.Background(), time.Now())
	require.Zero(t, gateway.calls.Load())
	s.fireDueSchedules(context.Background(), time.Now().Add(2*time.Hour))
	body := <-gateway.requests
	data, err := json.Marshal(body)
	require.NoError(t, err)
	require.Contains(t, string(data), "检查 CI")
	require.Contains(t, string(data), "work_report", "the unattended run can report to its tracked work")

	w = businessRequest(t, s, "GET", "/schedules/"+sc.ID, nil, 200)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &sc))
	require.NotNil(t, sc.LastRun)
	require.Empty(t, sc.LastRun.Error)
	require.Equal(t, "running", sc.RunState)
	first := sc.LastRun.SessionID
	work, err := s.app.Agents().WorkForSession(context.Background(), first)
	require.NoError(t, err)
	require.Equal(t, sc.LastRun.WorkID, work.ID)
	require.Equal(t, owner.ID, work.AgentID)
	require.True(t, strings.HasPrefix(work.Title, "巡检 · "))

	// A schedule never overlaps its own unfinished run.
	businessRequest(t, s, "POST", "/schedules/"+sc.ID+"/run", nil, 409)
	close(gateway.finish)
	require.Eventually(t, func() bool { return s.sessionRunState(first) == "completed" }, 3*time.Second, 10*time.Millisecond)

	w = businessRequest(t, s, "POST", "/schedules/"+sc.ID+"/run", nil, 202)
	var run agentdata.ScheduleRun
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &run))
	require.NotEqual(t, first, run.SessionID, "every firing starts a fresh session")
	require.Eventually(t, func() bool { return s.sessionRunState(run.SessionID) == "completed" }, 3*time.Second, 10*time.Millisecond)

	// A team-owned schedule runs as a team session led by the team leader.
	w = businessRequest(t, s, "POST", "/agents", map[string]any{"name": "处理员", "instructions": "处理 issue"}, 201)
	var member agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &member))
	w = businessRequest(t, s, "POST", "/teams", map[string]any{"name": "小组", "leader_id": owner.ID, "members": []any{map[string]any{"agent_id": member.ID}}}, 201)
	var team agentdata.Team
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &team))
	w = businessRequest(t, s, "POST", "/schedules", map[string]any{"name": "日报", "prompt": "汇总", "team_id": team.ID, "daily_at": "09:00"}, 201)
	var teamSchedule scheduleView
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &teamSchedule))
	w = businessRequest(t, s, "POST", "/schedules/"+teamSchedule.ID+"/run", nil, 202)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &run))
	require.Eventually(t, func() bool { return s.sessionRunState(run.SessionID) == "completed" }, 3*time.Second, 10*time.Millisecond)
	work, err = s.app.Agents().WorkForSession(context.Background(), run.SessionID)
	require.NoError(t, err)
	require.Equal(t, owner.ID, work.AgentID)
	<-gateway.requests // the manual agent run
	data, err = json.Marshal(<-gateway.requests)
	require.NoError(t, err)
	require.Contains(t, string(data), "team_delegate")

	w = businessRequest(t, s, "GET", "/schedules", nil, 200)
	require.Contains(t, w.Body.String(), sc.ID)
	businessRequest(t, s, "DELETE", "/schedules/"+sc.ID, nil, 204)
	businessRequest(t, s, "GET", "/schedules/"+sc.ID, nil, 404)
	s.SetAPIKey("owner-token")
	businessRequest(t, s, "GET", "/schedules", nil, 401)
}
