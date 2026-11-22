package agentdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ScheduleFiring is a durable intent to start one scheduled run.
type ScheduleFiring struct {
	Schedule  Schedule
	DueMS     int64
	SessionID string
	WorkID    string
}

// QueueScheduleFiring atomically records the occurrence and advances the plan.
// The original timestamp participates in the compare-and-swap, so stale readers
// cannot claim the same occurrence after another poller has advanced it.
func (s *Store) QueueScheduleFiring(ctx context.Context, sc Schedule, next time.Time) (bool, error) {
	db, err := s.open(ctx)
	if err != nil {
		return false, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE schedules SET next_run_ms=? WHERE id=? AND version=? AND next_run_ms=? AND next_run_ms>0`, next.UnixMilli(), sc.ID, sc.Version, sc.nextRunMS)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	snapshot, err := json.Marshal(sc)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schedule_firings(schedule_id,due_ms,definition) VALUES(?,?,?)`, sc.ID, sc.nextRunMS, string(snapshot))
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) PendingScheduleFirings(ctx context.Context) ([]ScheduleFiring, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT f.definition,f.due_ms,f.session_id,f.work_id FROM schedule_firings f JOIN schedules s ON s.id=f.schedule_id WHERE f.state='pending' AND s.version=json_extract(f.definition,'$.version') AND NOT EXISTS (SELECT 1 FROM schedule_firings earlier WHERE earlier.schedule_id=f.schedule_id AND earlier.state='pending' AND json_extract(earlier.definition,'$.version')=s.version AND earlier.due_ms<f.due_ms) ORDER BY f.due_ms`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduleFiring
	for rows.Next() {
		var f ScheduleFiring
		var data string
		if err = rows.Scan(&data, &f.DueMS, &f.SessionID, &f.WorkID); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &f.Schedule); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) SaveScheduleFiringSession(ctx context.Context, f ScheduleFiring, sessionID string) error {
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, `UPDATE schedule_firings SET session_id=? WHERE schedule_id=? AND due_ms=? AND state='pending' AND session_id=''`, sessionID, f.Schedule.ID, f.DueMS)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) FinishScheduleFiring(ctx context.Context, f ScheduleFiring, run ScheduleRun) error {
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE schedules SET last_run=? WHERE id=?`, string(data), f.Schedule.ID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE schedule_firings SET state='admitted',work_id=?,error='' WHERE schedule_id=? AND due_ms=?`, run.WorkID, f.Schedule.ID, f.DueMS)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// GetPendingScheduleFiring rechecks state after the scheduler lock is held.
func (s *Store) GetPendingScheduleFiring(ctx context.Context, id string, dueMS int64) (*ScheduleFiring, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var f ScheduleFiring
	var data string
	err = db.QueryRowContext(ctx, `SELECT f.definition,f.due_ms,f.session_id,f.work_id FROM schedule_firings f JOIN schedules s ON s.id=f.schedule_id WHERE f.schedule_id=? AND f.due_ms=? AND f.state='pending' AND s.version=json_extract(f.definition,'$.version')`, id, dueMS).Scan(&data, &f.DueMS, &f.SessionID, &f.WorkID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(data), &f.Schedule); err != nil {
		return nil, err
	}
	return &f, nil
}
