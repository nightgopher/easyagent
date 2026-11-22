package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/internal/agents/business"
	"github.com/nightgopher/easyagent/sdk/runtime"
)

// schedulePollInterval bounds how late a due schedule fires.
const schedulePollInterval = 30 * time.Second

// errScheduleBusy reports that the previous firing's session is still running
// or waiting for approval; schedules never overlap themselves.
var errScheduleBusy = errors.New("schedule's previous run is still in progress")

// scheduleRunNote is appended to every scheduled prompt. The run is
// unattended, so its result must land on the tracked work for acceptance.
const scheduleRunNote = `

---
这是定时任务「%s」的一次无人值守运行，没有人会实时回复你。完成后先用 work_get 读取版本，再用 work_report 汇报：有可验收的产出时设为 waiting_review，并在成果里登记文件或链接；需要用户决定时设为 waiting_answer 并写清问题；无法继续时设为 blocked。没有产出也如实说明，不要声称未做过的事。`

type scheduleView struct {
	agentdata.Schedule
	RunState string `json:"run_state,omitempty"`
}

func (s *Server) scheduleView(sc agentdata.Schedule) scheduleView {
	v := scheduleView{Schedule: sc}
	if sc.LastRun != nil && sc.LastRun.SessionID != "" {
		v.RunState = s.sessionRunState(sc.LastRun.SessionID)
	}
	return v
}

func (s *Server) sessionRunState(sessionID string) string {
	s.runs.mu.Lock()
	defer s.runs.mu.Unlock()
	if state := s.runs.sessions[sessionID]; state != nil && state.run != nil {
		return state.run.State
	}
	return ""
}

// runSchedules fires due schedules until the server context ends. It runs
// only in serve mode, so tests and embedded servers never fire on their own.
func (s *Server) runSchedules() {
	ticker := time.NewTicker(schedulePollInterval)
	defer ticker.Stop()
	for {
		s.fireDueSchedules(s.ctx, time.Now())
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) fireDueSchedules(ctx context.Context, now time.Time) {
	store := s.app.Agents()
	due, err := store.DueSchedules(ctx, now)
	if err != nil {
		slog.Error("schedules: list due", "error", err)
		return
	}
	for _, sc := range due {
		next, err := sc.Next(now)
		if err != nil {
			slog.Error("schedules: plan next", "schedule", sc.ID, "error", err)
			continue
		}
		claimed, err := store.QueueScheduleFiring(ctx, sc, next)
		if err != nil || !claimed {
			if err != nil {
				slog.Error("schedules: claim", "schedule", sc.ID, "error", err)
			}
			continue
		}
	}
	firings, err := store.PendingScheduleFirings(ctx)
	if err != nil {
		slog.Error("schedules: load pending firings", "error", err)
		return
	}
	for _, firing := range firings {
		if err := s.firePendingSchedule(ctx, firing); err != nil {
			slog.Warn("schedules: pending firing", "schedule", firing.Schedule.ID, "error", err)
		}
	}
}

// fireSchedule starts one run of sc in a fresh session and records it. It
// returns once the run is admitted; the run itself continues in the background.
func (s *Server) fireSchedule(ctx context.Context, sc agentdata.Schedule) (agentdata.ScheduleRun, error) {
	// Serialize firings so the overlap check and the new run cannot interleave
	// between the poller and a manual run.
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	if latest, err := s.app.Agents().GetSchedule(ctx, sc.ID); err == nil {
		sc.LastRun = latest.LastRun
	}
	if sc.LastRun != nil && sc.LastRun.SessionID != "" {
		switch s.sessionRunState(sc.LastRun.SessionID) {
		case "running", "waiting_confirmation":
			return agentdata.ScheduleRun{}, errScheduleBusy
		}
	}
	started := time.Now()
	run := agentdata.ScheduleRun{StartedAt: started.UTC().Format(time.RFC3339)}
	err := s.startScheduledRun(ctx, sc, started, &run)
	if err != nil {
		run.Error = err.Error()
	}
	if recordErr := s.app.Agents().RecordScheduleRun(ctx, sc.ID, run); recordErr != nil {
		slog.Error("schedules: record run", "schedule", sc.ID, "error", recordErr)
	}
	return run, err
}

func (s *Server) startScheduledRun(ctx context.Context, sc agentdata.Schedule, started time.Time, run *agentdata.ScheduleRun) error {
	return s.startScheduledRunWithFiring(ctx, sc, started, run, nil)
}

func (s *Server) firePendingSchedule(ctx context.Context, firing agentdata.ScheduleFiring) error {
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	current, err := s.app.Agents().GetPendingScheduleFiring(ctx, firing.Schedule.ID, firing.DueMS)
	if err != nil || current == nil {
		return err
	}
	firing = *current
	sc := firing.Schedule
	started := time.UnixMilli(firing.DueMS)
	if firing.SessionID != "" {
		requestID := "schedule-firing-" + sc.ID + "-" + fmt.Sprint(started.UnixMilli())
		receipt, err := s.loadRunReceipt(requestID)
		if err != nil {
			return err
		}
		if receipt != nil {
			if receipt.SessionID != firing.SessionID {
				return fmt.Errorf("schedule receipt session mismatch")
			}
			work, err := s.app.Agents().WorkForSession(ctx, firing.SessionID)
			if err != nil {
				return err
			}
			return s.app.Agents().FinishScheduleFiring(ctx, firing, agentdata.ScheduleRun{StartedAt: started.UTC().Format(time.RFC3339), SessionID: firing.SessionID, WorkID: work.ID})
		}
	}
	if latest, err := s.app.Agents().GetSchedule(ctx, sc.ID); err == nil && latest.LastRun != nil && latest.LastRun.SessionID != "" && latest.LastRun.SessionID != firing.SessionID {
		switch s.sessionRunState(latest.LastRun.SessionID) {
		case "running", "waiting_confirmation":
			return errScheduleBusy
		}
	}
	run := agentdata.ScheduleRun{StartedAt: started.UTC().Format(time.RFC3339), SessionID: firing.SessionID}
	if err := s.startScheduledRunWithFiring(ctx, sc, started, &run, &firing); err != nil {
		run.Error = err.Error()
		if recordErr := s.app.Agents().RecordScheduleRun(ctx, sc.ID, run); recordErr != nil {
			slog.Error("schedules: record pending failure", "schedule", sc.ID, "error", recordErr)
		}
		return err
	}
	return s.app.Agents().FinishScheduleFiring(ctx, firing, run)
}

func (s *Server) startScheduledRunWithFiring(ctx context.Context, sc agentdata.Schedule, started time.Time, run *agentdata.ScheduleRun, firing *agentdata.ScheduleFiring) error {
	cfg := s.app.Config()
	cfg.DefaultAccessMode = channelAccessMode(cfg, "")
	var deps runtime.Dependencies
	var team business.TeamSnapshot
	var err error
	agentID := sc.AgentID
	if sc.TeamID != "" {
		cfg, deps, team, err = s.app.TeamSessionSetup(ctx, sc.TeamID, cfg)
		agentID = team.LeaderID
	} else {
		cfg, deps, err = s.app.SessionSetup(ctx, "", sc.AgentID, cfg)
	}
	if err != nil {
		return err
	}
	var sess *runtime.AgentSession
	if run.SessionID != "" {
		sess, err = s.resolveSession(ctx, run.SessionID)
	} else {
		sess, err = s.app.SessionStore().Create(ctx, runtime.AgentSessionOptions{Config: cfg}, deps)
	}
	if err != nil {
		return err
	}
	if run.SessionID == "" {
		if sc.TeamID != "" {
			err = s.app.SessionManager().SaveTeamSession(sess.SessionID(), sess.Workspace(), team.LeaderID, team.ID, team.Name, team)
		} else {
			err = s.app.SessionManager().SaveAgentMeta(sess.SessionID(), sess.Workspace(), sc.AgentID)
		}
		if err != nil {
			_ = s.app.SessionStore().Delete(sess.SessionID())
			return fmt.Errorf("save session metadata: %w", err)
		}
	}
	run.SessionID = sess.SessionID()
	if firing != nil && firing.SessionID == "" {
		if err := s.app.Agents().SaveScheduleFiringSession(ctx, *firing, run.SessionID); err != nil {
			return err
		}
	}
	title := sc.Name + " · " + started.Format("01-02 15:04")
	work, err := s.app.Agents().TrackWork(ctx, agentdata.WorkItem{SessionID: sess.SessionID(), AgentID: agentID, Application: "business", Workspace: sess.Workspace(), WorkUpdate: agentdata.WorkUpdate{Title: title}})
	if err != nil {
		return fmt.Errorf("track work: %w", err)
	}
	run.WorkID = work.ID
	// Tools were built before the work existed; rebuild so work_report is bound.
	if err := sess.RefreshTools(ctx); err != nil {
		return fmt.Errorf("refresh tools: %w", err)
	}
	prompt := sc.Prompt + fmt.Sprintf(scheduleRunNote, sc.Name)
	requestID := "schedule-" + sc.ID + "-" + fmt.Sprint(started.UnixMilli())
	if firing != nil {
		requestID = "schedule-firing-" + sc.ID + "-" + fmt.Sprint(started.UnixMilli())
	}
	_, _, err = s.startRun(sess.SessionID(), prompt, requestID)
	return err
}

func (s *Server) schedules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Serialize changes with starting a queued firing, so an edit or delete
	// cannot race the decision to execute an earlier version.
	if r.Method != http.MethodGet {
		s.scheduleMu.Lock()
		defer s.scheduleMu.Unlock()
	}
	store, id := s.app.Agents(), r.PathValue("schedule")
	switch {
	case r.Method == http.MethodGet && id == "":
		items, err := store.ListSchedules(r.Context())
		if err != nil {
			agentDataError(w, err)
			return
		}
		views := make([]scheduleView, 0, len(items))
		for _, sc := range items {
			views = append(views, s.scheduleView(sc))
		}
		writeJSON(w, map[string]any{"schedules": views})
	case r.Method == http.MethodGet:
		sc, err := store.GetSchedule(r.Context(), id)
		if err != nil {
			agentDataError(w, err)
			return
		}
		writeJSON(w, s.scheduleView(sc))
	case r.Method == http.MethodDelete:
		if err := store.DeleteSchedule(r.Context(), id); err != nil {
			agentDataError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		var sc agentdata.Schedule
		if !decodeAgentJSON(w, r, &sc) {
			return
		}
		if sc.ID != "" && sc.ID != id {
			writeError(w, 400, "schedule id does not match request path")
			return
		}
		sc.ID = id
		saved, err := store.SaveSchedule(r.Context(), sc)
		if err != nil {
			agentDataError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		writeJSON(w, s.scheduleView(saved))
	}
}

// runScheduleNow fires a schedule immediately without moving its plan.
func (s *Server) runScheduleNow(w http.ResponseWriter, r *http.Request) {
	sc, err := s.app.Agents().GetSchedule(r.Context(), r.PathValue("schedule"))
	if err != nil {
		agentDataError(w, err)
		return
	}
	run, err := s.fireSchedule(r.Context(), sc)
	if errors.Is(err, errScheduleBusy) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		agentDataError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, run)
}
