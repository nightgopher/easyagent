package agentdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// WorkItem is an explicitly tracked piece of work, independent of any single
// model turn. Its owning session and agent never change.
type WorkItem struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	AgentID     string `json:"agent_id,omitempty"`
	Application string `json:"application"`
	Workspace   string `json:"workspace"`
	WorkUpdate
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

type WorkArtifact struct {
	Title string `json:"title"`
	Kind  string `json:"kind"` // file (workspace-relative path) or url (HTTP/S)
	Value string `json:"value"`
}

type WorkUpdate struct {
	Title     string         `json:"title"`
	State     string         `json:"state"`
	Summary   string         `json:"summary"`
	Question  string         `json:"question"`
	Artifacts []WorkArtifact `json:"artifacts"`
	Version   int64          `json:"version"`
}

const workSchema = `
CREATE TABLE IF NOT EXISTS work_items (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL UNIQUE,
 value TEXT NOT NULL, version INTEGER NOT NULL, updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS work_updated ON work_items(updated_at DESC, id);
`

func (w WorkItem) Closed() bool { return w.State == "completed" || w.State == "cancelled" }

func validWorkText(s string, limit int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= limit && !strings.ContainsRune(s, 0)
}

func normalizeWork(v *WorkUpdate) error {
	v.Title, v.Summary, v.Question = strings.TrimSpace(v.Title), strings.TrimSpace(v.Summary), strings.TrimSpace(v.Question)
	if v.Title == "" || !validWorkText(v.Title, 160) || !validWorkText(v.Summary, 4000) || !validWorkText(v.Question, 1000) {
		return fmt.Errorf("invalid work title, summary or question")
	}
	if !slices.Contains([]string{"active", "waiting_answer", "waiting_review", "blocked", "completed", "cancelled"}, v.State) {
		return fmt.Errorf("invalid work state")
	}
	if (v.State == "waiting_answer" || v.State == "blocked") && v.Question == "" {
		return fmt.Errorf("a question or blocker is required")
	}
	if v.State != "waiting_answer" && v.State != "blocked" {
		v.Question = ""
	}
	if len(v.Artifacts) > 16 {
		return fmt.Errorf("at most 16 artifacts")
	}
	if v.Artifacts == nil {
		v.Artifacts = []WorkArtifact{}
	}
	for _, a := range v.Artifacts {
		if strings.TrimSpace(a.Title) == "" || !validWorkText(a.Title, 160) || !validWorkText(a.Value, 2048) {
			return fmt.Errorf("invalid artifact")
		}
		switch a.Kind {
		case "file":
			if a.Value == "" || a.Value == "." || path.IsAbs(a.Value) || path.Clean(a.Value) != a.Value || strings.ContainsAny(a.Value, "\\:\r\n") || a.Value == ".." || strings.HasPrefix(a.Value, "../") {
				return fmt.Errorf("artifact file must be a relative workspace path")
			}
		case "url":
			u, err := url.Parse(a.Value)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
				return fmt.Errorf("artifact URL must use HTTP or HTTPS without credentials")
			}
		default:
			return fmt.Errorf("invalid artifact kind")
		}
	}
	return nil
}

// TrackWork is idempotent per session. Identity comes from host session metadata,
// never from model arguments. Existing tracking is not overwritten on retries.
func (s *Store) TrackWork(ctx context.Context, w WorkItem) (WorkItem, error) {
	if w.SessionID == "" {
		return WorkItem{}, fmt.Errorf("session is required")
	}
	w.State, w.Version = "active", 1
	if err := normalizeWork(&w.WorkUpdate); err != nil {
		return WorkItem{}, err
	}
	w.ID, w.UpdatedBy = "work-"+uuid.NewString(), "user"
	w.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	w.UpdatedAt = w.CreatedAt
	db, err := s.open(ctx)
	if err != nil {
		return WorkItem{}, err
	}
	defer func() { _ = db.Close() }()
	data, err := json.Marshal(w)
	if err != nil {
		return WorkItem{}, err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO work_items VALUES(?,?,?,?,?) ON CONFLICT(session_id) DO NOTHING", w.ID, w.SessionID, string(data), w.Version, w.UpdatedAt)
	if err != nil {
		return WorkItem{}, err
	}
	return readWork(db.QueryRowContext(ctx, "SELECT value FROM work_items WHERE session_id=?", w.SessionID))
}

func readWork(row interface{ Scan(...any) error }) (WorkItem, error) {
	var data string
	if err := row.Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorkItem{}, ErrNotFound
		}
		return WorkItem{}, err
	}
	var w WorkItem
	err := json.Unmarshal([]byte(data), &w)
	return w, err
}

func (s *Store) WorkForSession(ctx context.Context, sessionID string) (WorkItem, error) {
	db, err := s.open(ctx)
	if err != nil {
		return WorkItem{}, err
	}
	defer func() { _ = db.Close() }()
	return readWork(db.QueryRowContext(ctx, "SELECT value FROM work_items WHERE session_id=?", sessionID))
}

func (s *Store) GetWork(ctx context.Context, id string) (WorkItem, error) {
	db, err := s.open(ctx)
	if err != nil {
		return WorkItem{}, err
	}
	defer func() { _ = db.Close() }()
	return readWork(db.QueryRowContext(ctx, "SELECT value FROM work_items WHERE id=?", id))
}

func (s *Store) ListWork(ctx context.Context, query, sessionID string, limit, offset int) ([]WorkItem, error) {
	if !validWorkText(query, 256) || limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return nil, fmt.Errorf("invalid work query")
	}
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT value FROM work_items WHERE (?='' OR session_id=?) AND
	(?='' OR instr(lower(json_extract(value,'$.title') || ' ' || json_extract(value,'$.summary') || ' ' || json_extract(value,'$.artifacts')),lower(?))>0)
	ORDER BY updated_at DESC, id LIMIT ? OFFSET ?`, sessionID, sessionID, query, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []WorkItem{}
	for rows.Next() {
		w, err := readWork(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, w)
	}
	return items, rows.Err()
}

// A model can report only its session's open work; only a user can close or
// reopen it. Optimistic versions prevent a late report overwriting user edits.
func (s *Store) UpdateWork(ctx context.Context, id, sessionID string, v WorkUpdate) (WorkItem, error) {
	if err := normalizeWork(&v); err != nil {
		return WorkItem{}, err
	}
	db, err := s.open(ctx)
	if err != nil {
		return WorkItem{}, err
	}
	defer func() { _ = db.Close() }()
	w, err := readWork(db.QueryRowContext(ctx, "SELECT value FROM work_items WHERE id=?", id))
	if err != nil {
		return WorkItem{}, err
	}
	if sessionID != "" && w.SessionID != sessionID {
		return WorkItem{}, ErrNotFound
	}
	if w.Version != v.Version {
		return WorkItem{}, ErrConflict
	}
	if sessionID != "" && (w.Closed() || v.State == "completed" || v.State == "cancelled") {
		return WorkItem{}, fmt.Errorf("only the user can close or reopen work")
	}
	w.WorkUpdate = v
	w.Version++
	w.UpdatedBy = "user"
	if sessionID != "" {
		w.UpdatedBy = "agent"
	}
	w.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(w)
	if err != nil {
		return WorkItem{}, err
	}
	result, err := db.ExecContext(ctx, "UPDATE work_items SET value=?,version=?,updated_at=? WHERE id=? AND version=?", string(data), w.Version, w.UpdatedAt, id, v.Version)
	if err != nil {
		return WorkItem{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return WorkItem{}, err
	}
	if n == 0 {
		return WorkItem{}, ErrConflict
	}
	return w, nil
}
