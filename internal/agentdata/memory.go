package agentdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Memory struct {
	ID            string `json:"id"`
	AgentID       string `json:"agent_id"`
	Key           string `json:"key"`
	Content       string `json:"content"`
	Scope         string `json:"scope"` // agent or workspace
	Workspace     string `json:"workspace,omitempty"`
	SourceKind    string `json:"source_kind"`
	SourceSession string `json:"source_session,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Version       int64  `json:"version"`
	Expired       bool   `json:"expired"`
}

const memorySchema = `
CREATE TABLE IF NOT EXISTS agent_memories (
 rowid INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, agent_id TEXT NOT NULL,
 key TEXT NOT NULL, content TEXT NOT NULL, scope TEXT NOT NULL, workspace TEXT NOT NULL,
 source_kind TEXT NOT NULL, source_session TEXT NOT NULL, created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL, expires_at INTEGER NOT NULL DEFAULT 0, version INTEGER NOT NULL,
 UNIQUE(agent_id,scope,workspace,key));
CREATE INDEX IF NOT EXISTS memory_agent_scope ON agent_memories(agent_id,scope,workspace);
CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(key,content,content='agent_memories',content_rowid='rowid',tokenize='trigram');
CREATE TRIGGER IF NOT EXISTS memory_insert AFTER INSERT ON agent_memories BEGIN
 INSERT INTO memory_fts(rowid,key,content) VALUES(new.rowid,new.key,new.content); END;
CREATE TRIGGER IF NOT EXISTS memory_delete AFTER DELETE ON agent_memories BEGIN
 INSERT INTO memory_fts(memory_fts,rowid,key,content) VALUES('delete',old.rowid,old.key,old.content); END;
CREATE TRIGGER IF NOT EXISTS memory_update AFTER UPDATE ON agent_memories BEGIN
 INSERT INTO memory_fts(memory_fts,rowid,key,content) VALUES('delete',old.rowid,old.key,old.content);
 INSERT INTO memory_fts(rowid,key,content) VALUES(new.rowid,new.key,new.content); END;
`

const memoryColumns = `m.id,m.agent_id,m.key,m.content,m.scope,m.workspace,m.source_kind,m.source_session,m.created_at,m.updated_at,m.expires_at,m.version`

func scanMemory(row interface{ Scan(...any) error }) (Memory, error) {
	var m Memory
	var expires int64
	err := row.Scan(&m.ID, &m.AgentID, &m.Key, &m.Content, &m.Scope, &m.Workspace, &m.SourceKind, &m.SourceSession, &m.CreatedAt, &m.UpdatedAt, &expires, &m.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, ErrNotFound
	}
	if expires != 0 {
		m.ExpiresAt = time.Unix(expires, 0).UTC().Format(time.RFC3339)
		m.Expired = expires <= time.Now().Unix()
	}
	return m, err
}

// SaveMemory binds ownership and provenance from the caller, never the model's
// JSON. Updates require a version and keep the original creation time.
func (s *Store) SaveMemory(ctx context.Context, agentID, workspace, kind, session string, m Memory) (Memory, error) {
	a, err := s.GetAgent(ctx, agentID)
	if err != nil {
		return Memory{}, err
	}
	if a.Disabled || !a.MemoryEnabled {
		return Memory{}, fmt.Errorf("agent memory is not enabled")
	}
	m.AgentID, m.SourceKind, m.SourceSession = agentID, kind, session
	if kind != "user" && kind != "agent" {
		return Memory{}, fmt.Errorf("invalid memory source")
	}
	m.Key = strings.TrimSpace(m.Key)
	m.Content = strings.TrimSpace(m.Content)
	if m.Key == "" || len([]rune(m.Key)) > 120 || m.Content == "" || len([]rune(m.Content)) > 4000 {
		return Memory{}, fmt.Errorf("memory requires a key (up to 120 characters) and content (up to 4000 characters)")
	}
	if m.Scope == "" {
		m.Scope = "agent"
	}
	m.Workspace = ""
	if m.Scope == "workspace" {
		if workspace == "" || !filepath.IsAbs(workspace) {
			return Memory{}, fmt.Errorf("workspace memory requires a bound workspace")
		}
		m.Workspace = filepath.Clean(workspace)
	} else if m.Scope != "agent" {
		return Memory{}, fmt.Errorf("scope must be agent or workspace")
	}
	var expires int64
	if m.ExpiresAt != "" {
		stamp, err := time.Parse(time.RFC3339, m.ExpiresAt)
		if err != nil {
			return Memory{}, fmt.Errorf("expires_at must be an RFC3339 timestamp")
		}
		expires = stamp.Unix()
		m.ExpiresAt = stamp.UTC().Format(time.RFC3339)
	}
	db, err := s.open(ctx)
	if err != nil {
		return Memory{}, err
	}
	defer func() { _ = db.Close() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if m.ID == "" {
		m.ID, m.Version, m.CreatedAt, m.UpdatedAt = "mem-"+uuid.NewString(), 1, now, now
		result, err := db.ExecContext(ctx, `INSERT INTO agent_memories(id,agent_id,key,content,scope,workspace,source_kind,source_session,created_at,updated_at,expires_at,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,1) ON CONFLICT(agent_id,scope,workspace,key) DO NOTHING`, m.ID, agentID, m.Key, m.Content, m.Scope, m.Workspace, kind, session, now, now, expires)
		if err != nil {
			return Memory{}, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return Memory{}, err
		}
		if count == 0 {
			old, err := scanMemory(db.QueryRowContext(ctx, "SELECT "+memoryColumns+" FROM agent_memories m WHERE m.agent_id=? AND m.scope=? AND m.workspace=? AND m.key=?", agentID, m.Scope, m.Workspace, m.Key))
			if err != nil {
				return Memory{}, err
			}
			if old.Content == m.Content && old.ExpiresAt == m.ExpiresAt {
				return old, nil
			}
			return Memory{}, ErrConflict
		}
	} else {
		old, err := scanMemory(db.QueryRowContext(ctx, "SELECT "+memoryColumns+" FROM agent_memories m WHERE m.agent_id=? AND m.id=?", agentID, m.ID))
		if err != nil {
			return Memory{}, err
		}
		if old.Scope != m.Scope || old.Workspace != m.Workspace || old.Key != m.Key {
			return Memory{}, fmt.Errorf("memory identity or scope cannot be changed; create a new memory instead")
		}
		result, err := db.ExecContext(ctx, `UPDATE agent_memories SET content=?,source_kind=?,source_session=?,updated_at=?,expires_at=?,version=version+1 WHERE agent_id=? AND id=? AND version=?`, m.Content, kind, session, now, expires, agentID, m.ID, m.Version)
		if err != nil {
			return Memory{}, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return Memory{}, err
		}
		if count == 0 {
			return Memory{}, ErrConflict
		}
	}
	return scanMemory(db.QueryRowContext(ctx, "SELECT "+memoryColumns+" FROM agent_memories m WHERE m.agent_id=? AND m.id=?", agentID, m.ID))
}

func (s *Store) GetMemory(ctx context.Context, agentID, id string) (Memory, error) {
	db, err := s.open(ctx)
	if err != nil {
		return Memory{}, err
	}
	defer func() { _ = db.Close() }()
	return scanMemory(db.QueryRowContext(ctx, "SELECT "+memoryColumns+" FROM agent_memories m WHERE m.agent_id=? AND m.id=?", agentID, id))
}

func (s *Store) DeleteMemory(ctx context.Context, agentID, id string, version int64) error {
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	result, err := db.ExecContext(ctx, "DELETE FROM agent_memories WHERE agent_id=? AND id=? AND version=?", agentID, id, version)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrConflict
	}
	return err
}

// ListMemories is the owner-facing management view, including expired records.
func (s *Store) ListMemories(ctx context.Context, agentID string, query string, offset int) ([]Memory, bool, error) {
	if _, err := s.GetAgent(ctx, agentID); err != nil {
		return nil, false, err
	}
	if offset < 0 || len(query) > 512 {
		return nil, false, fmt.Errorf("invalid memory query or offset")
	}
	db, err := s.open(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = db.Close() }()
	items, err := queryMemories(ctx, db, "SELECT "+memoryColumns+" FROM agent_memories m WHERE m.agent_id=? AND instr(lower(m.key || ' ' || m.content),lower(?))>0 ORDER BY m.updated_at DESC,m.id LIMIT 51 OFFSET ?", agentID, strings.TrimSpace(query), offset)
	more := len(items) > 50
	if more {
		items = items[:50]
	}
	return items, more, err
}

func queryMemories(ctx context.Context, db *sql.DB, statement string, args ...any) ([]Memory, error) {
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []Memory{}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	return items, rows.Err()
}

// SearchMemories applies identity, workspace and expiry in SQL before ranking
// and limiting. Agent IDs and workspace bindings are never model parameters.
func (s *Store) SearchMemories(ctx context.Context, agentID, workspace, query string) ([]Memory, error) {
	a, err := s.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if a.Disabled || !a.MemoryEnabled {
		return nil, fmt.Errorf("agent memory is not enabled")
	}
	if len(query) > 512 {
		return nil, fmt.Errorf("query is too long")
	}
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	statement := "SELECT " + memoryColumns + " FROM agent_memories m"
	where := " WHERE m.agent_id=? AND (m.scope='agent' OR (m.scope='workspace' AND m.workspace=?)) AND (m.expires_at=0 OR m.expires_at>?)"
	args := []any{agentID, filepath.Clean(workspace), time.Now().Unix()}
	terms := strings.Fields(query)
	useFTS := len(terms) > 0
	for _, term := range terms {
		if utf8.RuneCountInString(term) < 3 {
			useFTS = false
		}
	}
	order := " ORDER BY m.updated_at DESC"
	if useFTS {
		statement += " JOIN memory_fts ON memory_fts.rowid=m.rowid"
		quoted := make([]string, 0, len(terms))
		for _, term := range terms {
			quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
		}
		where += " AND memory_fts MATCH ?"
		args = append(args, strings.Join(quoted, " AND "))
		order = " ORDER BY rank,m.updated_at DESC"
	} else {
		for _, term := range terms {
			where += " AND instr(lower(m.key || ' ' || m.content),lower(?))>0"
			args = append(args, term)
		}
	}
	return queryMemories(ctx, db, statement+where+order+" LIMIT 8", args...)
}
