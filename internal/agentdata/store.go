// Package agentdata persists host-owned business agents and their scoped data.
// Session transcripts and user-maintained knowledge documents remain in their
// existing stores. This database is not a disposable search cache.
package agentdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("agent or resource not found")
	ErrConflict = errors.New("configuration changed; reload before saving")
	ErrDisabled = errors.New("agent is disabled")
)

type Agent struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Instructions string   `json:"instructions"`
	Model        string   `json:"model,omitempty"`
	Workspace    string   `json:"workspace,omitempty"`
	Tools        []string `json:"tools"`
	Skills       []string `json:"skills"`
	Disabled     bool     `json:"disabled"`
	Version      int64    `json:"version"`
	UpdatedAt    string   `json:"updated_at"`
}

type Store struct{ dataDir string }

func New(dataDir string) *Store { return &Store{dataDir: dataDir} }

func DefaultTools() []string {
	return []string{"read", "write", "ask_user_question", "local_time", "search_tools"}
}

func (s *Store) Workspace(a Agent) string {
	if a.Workspace != "" {
		return a.Workspace
	}
	return filepath.Join(s.dataDir, "agents", "workspaces", a.ID)
}

const schema = `
CREATE TABLE IF NOT EXISTS business_agents (
 id TEXT PRIMARY KEY, definition TEXT NOT NULL, version INTEGER NOT NULL
);
`

func (s *Store) open(ctx context.Context) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.dataDir, "agents")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile("agents.sqlite", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "agents.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;"+schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func normalize(a *Agent) error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || len([]rune(a.Name)) > 100 {
		return fmt.Errorf("name must contain 1–100 characters")
	}
	if strings.TrimSpace(a.Instructions) == "" || len(a.Instructions) > 48*1024 {
		return fmt.Errorf("instructions must contain 1–49152 bytes")
	}
	a.Model = strings.TrimSpace(a.Model)
	if len(a.Model) > 200 {
		return fmt.Errorf("model is too long")
	}
	if a.Workspace != "" {
		if !filepath.IsAbs(a.Workspace) {
			return fmt.Errorf("workspace must be an absolute path on this host")
		}
		a.Workspace = filepath.Clean(a.Workspace)
	}
	if a.Tools == nil {
		a.Tools = DefaultTools()
	}
	if a.Skills == nil {
		a.Skills = []string{}
	}
	for _, list := range [][]string{a.Tools, a.Skills} {
		if len(list) > 128 {
			return fmt.Errorf("at most 128 tools or skills may be selected")
		}
		seen := map[string]bool{}
		for _, name := range list {
			if name == "" || len(name) > 200 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\r\n\x00") || seen[name] {
				return fmt.Errorf("invalid or duplicate tool/skill name")
			}
			seen[name] = true
		}
	}
	return nil
}

func (s *Store) SaveAgent(ctx context.Context, a Agent) (Agent, error) {
	if err := normalize(&a); err != nil {
		return Agent{}, err
	}
	create := a.ID == ""
	if create {
		a.ID, a.Version = "agent-"+uuid.NewString(), 0
	}
	previous := a.Version
	a.Version++
	a.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(a)
	if err != nil {
		return Agent{}, err
	}
	db, err := s.open(ctx)
	if err != nil {
		return Agent{}, err
	}
	defer func() { _ = db.Close() }()
	if create {
		_, err = db.ExecContext(ctx, "INSERT INTO business_agents(id,definition,version) VALUES(?,?,?)", a.ID, string(data), a.Version)
	} else {
		var result sql.Result
		result, err = db.ExecContext(ctx, "UPDATE business_agents SET definition=?,version=? WHERE id=? AND version=?", string(data), a.Version, a.ID, previous)
		if err == nil {
			var count int64
			count, err = result.RowsAffected()
			if err == nil && count == 0 {
				err = ErrConflict
			}
		}
	}
	return a, err
}

func (s *Store) GetAgent(ctx context.Context, id string) (Agent, error) {
	db, err := s.open(ctx)
	if err != nil {
		return Agent{}, err
	}
	defer func() { _ = db.Close() }()
	var data string
	if err := db.QueryRowContext(ctx, "SELECT definition FROM business_agents WHERE id=?", id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Agent{}, ErrNotFound
		}
		return Agent{}, err
	}
	var a Agent
	err = json.Unmarshal([]byte(data), &a)
	return a, err
}

func (s *Store) ListAgents(ctx context.Context) ([]Agent, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, "SELECT definition FROM business_agents ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []Agent{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var a Agent
		if err := json.Unmarshal([]byte(data), &a); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
