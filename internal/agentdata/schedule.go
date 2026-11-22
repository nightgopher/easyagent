package agentdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Schedule is a durable recurring task owned by a saved agent or team, not by
// a conversation. Each run starts a fresh session with bounded context and is
// tracked as work, so its outcome waits in the work inbox for acceptance.
type Schedule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	AgentID string `json:"agent_id,omitempty"`
	TeamID  string `json:"team_id,omitempty"`
	Prompt  string `json:"prompt"`
	// Exactly one of Every (a Go duration such as "2h") or DailyAt ("HH:MM",
	// host local time) sets the timing.
	Every     string `json:"every,omitempty"`
	DailyAt   string `json:"daily_at,omitempty"`
	Disabled  bool   `json:"disabled"`
	Version   int64  `json:"version"`
	UpdatedAt string `json:"updated_at"`
	// Runner-owned fields: filled on read, ignored on save.
	NextRunAt string       `json:"next_run_at,omitempty"`
	LastRun   *ScheduleRun `json:"last_run,omitempty"`
	nextRunMS int64
}

// ScheduleRun records the latest firing. Live run state comes from the run
// registry through the session, never from this record.
type ScheduleRun struct {
	StartedAt string `json:"started_at"`
	SessionID string `json:"session_id,omitempty"`
	WorkID    string `json:"work_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// MinScheduleInterval keeps unattended model runs from firing too often.
const MinScheduleInterval = 5 * time.Minute

const scheduleSchema = `
CREATE TABLE IF NOT EXISTS schedules (
	id TEXT PRIMARY KEY,
	definition TEXT NOT NULL,
	version INTEGER NOT NULL,
	next_run_ms INTEGER NOT NULL DEFAULT 0,
	last_run TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS schedules_due ON schedules(next_run_ms);
CREATE TABLE IF NOT EXISTS schedule_firings (
 schedule_id TEXT NOT NULL, due_ms INTEGER NOT NULL, definition TEXT NOT NULL,
 session_id TEXT NOT NULL DEFAULT '', work_id TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'pending', error TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(schedule_id, due_ms)
);
`

func normalizeSchedule(sc *Schedule) error {
	sc.Name = strings.TrimSpace(sc.Name)
	if sc.Name == "" || utf8.RuneCountInString(sc.Name) > 100 {
		return fmt.Errorf("name must contain 1–100 characters")
	}
	sc.Prompt = strings.TrimSpace(sc.Prompt)
	if sc.Prompt == "" || len(sc.Prompt) > 16*1024 || !utf8.ValidString(sc.Prompt) {
		return fmt.Errorf("prompt must contain 1–16384 bytes")
	}
	sc.AgentID, sc.TeamID = strings.TrimSpace(sc.AgentID), strings.TrimSpace(sc.TeamID)
	if (sc.AgentID == "") == (sc.TeamID == "") {
		return fmt.Errorf("exactly one of agent_id or team_id is required")
	}
	sc.Every, sc.DailyAt = strings.TrimSpace(sc.Every), strings.TrimSpace(sc.DailyAt)
	if (sc.Every == "") == (sc.DailyAt == "") {
		return fmt.Errorf("exactly one of every or daily_at is required")
	}
	_, err := sc.Next(time.Now())
	return err
}

// Next returns the first firing strictly after the given time.
func (sc Schedule) Next(after time.Time) (time.Time, error) {
	if sc.Every != "" {
		d, err := time.ParseDuration(sc.Every)
		if err != nil || d < MinScheduleInterval || d > 31*24*time.Hour {
			return time.Time{}, fmt.Errorf("every must be a duration between 5m and 744h")
		}
		return after.Add(d), nil
	}
	clock, err := time.Parse("15:04", sc.DailyAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("daily_at must use HH:MM")
	}
	local := after.In(time.Local)
	next := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), 0, 0, time.Local)
	if !next.After(after) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}

// SaveSchedule creates or updates a schedule. The owner must exist and be
// enabled. Saving re-plans the next firing from now; disabling clears it.
func (s *Store) SaveSchedule(ctx context.Context, sc Schedule) (Schedule, error) {
	if err := normalizeSchedule(&sc); err != nil {
		return Schedule{}, err
	}
	if sc.AgentID != "" {
		owner, err := s.GetAgent(ctx, sc.AgentID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return Schedule{}, fmt.Errorf("agent %q does not exist", sc.AgentID)
			}
			return Schedule{}, err
		}
		if owner.Disabled {
			return Schedule{}, fmt.Errorf("agent %q is disabled", sc.AgentID)
		}
	} else {
		owner, err := s.GetTeam(ctx, sc.TeamID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return Schedule{}, fmt.Errorf("team %q does not exist", sc.TeamID)
			}
			return Schedule{}, err
		}
		if owner.Disabled {
			return Schedule{}, fmt.Errorf("team %q is disabled", sc.TeamID)
		}
	}
	create := sc.ID == ""
	if create {
		sc.ID, sc.Version = "schedule-"+uuid.NewString(), 0
	}
	previous := sc.Version
	sc.Version++
	now := time.Now()
	sc.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	sc.NextRunAt, sc.LastRun = "", nil
	var nextMS int64
	if !sc.Disabled {
		next, err := sc.Next(now)
		if err != nil {
			return Schedule{}, err
		}
		nextMS = next.UnixMilli()
	}
	data, err := json.Marshal(sc)
	if err != nil {
		return Schedule{}, err
	}
	db, err := s.open(ctx)
	if err != nil {
		return Schedule{}, err
	}
	defer func() { _ = db.Close() }()
	if create {
		_, err = db.ExecContext(ctx, "INSERT INTO schedules(id,definition,version,next_run_ms) VALUES(?,?,?,?)", sc.ID, string(data), sc.Version, nextMS)
	} else {
		var result sql.Result
		result, err = db.ExecContext(ctx, "UPDATE schedules SET definition=?,version=?,next_run_ms=? WHERE id=? AND version=?", string(data), sc.Version, nextMS, sc.ID, previous)
		if err == nil {
			var count int64
			count, err = result.RowsAffected()
			if err == nil && count == 0 {
				err = ErrConflict
			}
		}
	}
	if err != nil {
		return Schedule{}, err
	}
	return readSchedule(db.QueryRowContext(ctx, "SELECT definition,next_run_ms,last_run FROM schedules WHERE id=?", sc.ID))
}

func readSchedule(row interface{ Scan(...any) error }) (Schedule, error) {
	var definition, lastRun string
	var nextMS int64
	if err := row.Scan(&definition, &nextMS, &lastRun); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Schedule{}, ErrNotFound
		}
		return Schedule{}, err
	}
	var sc Schedule
	if err := json.Unmarshal([]byte(definition), &sc); err != nil {
		return Schedule{}, fmt.Errorf("decode schedule: %w", err)
	}
	if nextMS > 0 {
		sc.nextRunMS = nextMS
		sc.NextRunAt = time.UnixMilli(nextMS).UTC().Format(time.RFC3339)
	}
	if lastRun != "" {
		sc.LastRun = &ScheduleRun{}
		if err := json.Unmarshal([]byte(lastRun), sc.LastRun); err != nil {
			return Schedule{}, fmt.Errorf("decode schedule run: %w", err)
		}
	}
	return sc, nil
}

func (s *Store) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	db, err := s.open(ctx)
	if err != nil {
		return Schedule{}, err
	}
	defer func() { _ = db.Close() }()
	return readSchedule(db.QueryRowContext(ctx, "SELECT definition,next_run_ms,last_run FROM schedules WHERE id=?", id))
}

func (s *Store) ListSchedules(ctx context.Context) ([]Schedule, error) {
	return s.querySchedules(ctx, "SELECT definition,next_run_ms,last_run FROM schedules ORDER BY id")
}

// DueSchedules lists enabled schedules whose next firing is not after now.
// Firings missed while the host was down come back once, not once per miss.
func (s *Store) DueSchedules(ctx context.Context, now time.Time) ([]Schedule, error) {
	return s.querySchedules(ctx, "SELECT definition,next_run_ms,last_run FROM schedules WHERE next_run_ms>0 AND next_run_ms<=? ORDER BY next_run_ms, id", now.UnixMilli())
}

func (s *Store) querySchedules(ctx context.Context, query string, args ...any) ([]Schedule, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []Schedule{}
	for rows.Next() {
		sc, err := readSchedule(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, sc)
	}
	return items, rows.Err()
}

// AdvanceSchedule claims a due firing by moving it to next. It only succeeds
// while the firing read as due is still planned, so a user edit or disable in
// between wins and the runner skips that firing.
func (s *Store) AdvanceSchedule(ctx context.Context, sc Schedule, next time.Time) (bool, error) {
	db, err := s.open(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()
	result, err := db.ExecContext(ctx, "UPDATE schedules SET next_run_ms=? WHERE id=? AND version=? AND next_run_ms=? AND next_run_ms>0", next.UnixMilli(), sc.ID, sc.Version, sc.nextRunMS)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

// RecordScheduleRun stores the latest firing without touching the definition
// or version, so it never conflicts with a user edit.
func (s *Store) RecordScheduleRun(ctx context.Context, id string, run ScheduleRun) error {
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(ctx, "UPDATE schedules SET last_run=? WHERE id=?", string(data), id)
	return err
}

func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	result, err := db.ExecContext(ctx, "DELETE FROM schedules WHERE id=?", id)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count == 0 {
		return ErrNotFound
	}
	return nil
}
