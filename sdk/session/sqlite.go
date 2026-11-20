package session

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStorage stores an append-only session and its active cursor in one
// transactional database. A legacy JSONL file, when present, is imported once
// in the same transaction as the migration marker and is never modified.
type SQLiteStorage struct {
	path, legacyPath string
	legacyStamp      string
	legacyBound      bool
	db               *sql.DB
	mu               sync.Mutex
	forked           bool
	forkLeaf         string
}

func NewSQLiteStorage(path, legacyPath string) *SQLiteStorage {
	return &SQLiteStorage{path: path, legacyPath: legacyPath}
}

func (s *SQLiteStorage) Init() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	uri, err := SQLiteFileURI(s.path)
	if err != nil {
		return err
	}
	s.db, err = sql.Open("sqlite", uri)
	if err != nil {
		return err
	}
	s.db.SetMaxOpenConns(1)
	if _, err = s.db.Exec(`PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS entries (seq INTEGER PRIMARY KEY, id TEXT NOT NULL, parent_id TEXT NOT NULL, type TEXT NOT NULL, timestamp INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS entry_ids ON entries(id, seq DESC);
CREATE TABLE IF NOT EXISTS state (key TEXT PRIMARY KEY, value TEXT NOT NULL);`); err != nil {
		_ = s.db.Close()
		return err
	}
	if err = s.migrate(); err != nil {
		_ = s.db.Close()
		return err
	}
	return nil
}

// SQLiteFileURI preserves literal path characters that SQLite otherwise
// interprets as URI parameters, including question marks and percent signs.
func SQLiteFileURI(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path = filepath.ToSlash(abs)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path}
	return u.String(), nil
}

func (s *SQLiteStorage) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var marker string
	err = tx.QueryRow("SELECT value FROM state WHERE key='initialized'").Scan(&marker)
	if err == nil {
		if marker != "1" {
			return fmt.Errorf("unsupported session database version %q", marker)
		}
		if s.legacyPath != "" {
			var saved string
			if e := tx.QueryRow("SELECT value FROM state WHERE key='legacy_signature'").Scan(&saved); e == nil {
				current, e := legacySignature(s.legacyPath)
				if e != nil {
					return e
				}
				if current != saved {
					return fmt.Errorf("legacy session changed after SQLite migration; refusing to discard divergent history")
				}
				s.legacyStamp, s.legacyBound = saved, true
			} else if e != sql.ErrNoRows {
				return e
			}
		}
		return tx.Commit()
	}
	if err != sql.ErrNoRows {
		return err
	}
	legacyStamp := "absent"
	if s.legacyPath != "" {
		f, err := os.Open(s.legacyPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			defer f.Close()
			before, err := f.Stat()
			if err != nil {
				return err
			}
			legacyStamp = fmt.Sprintf("%d/%d", before.Size(), before.ModTime().UnixNano())
			reader := bufio.NewReader(f)
			for {
				line, readErr := reader.ReadBytes('\n')
				if len(line) > 0 {
					var entry Entry
					if err = json.Unmarshal(line, &entry); err != nil {
						return fmt.Errorf("import legacy session: %w", err)
					}
					if err = sqliteAppend(context.Background(), tx, entry, true, false); err != nil {
						return err
					}
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					return readErr
				}
			}
			after, err := os.Stat(s.legacyPath)
			if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
				return fmt.Errorf("legacy session changed during migration")
			}
		}
	}
	if s.legacyPath != "" {
		signature, err := legacySignature(s.legacyPath)
		if err != nil {
			return err
		}
		if signature != legacyStamp {
			return fmt.Errorf("legacy session changed during migration")
		}
		if _, err = tx.Exec("INSERT INTO state(key,value) VALUES('legacy_signature',?)", signature); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT INTO state(key,value) VALUES('initialized','1')"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if s.legacyPath != "" {
		s.legacyStamp, s.legacyBound = legacyStamp, true
	}
	return nil
}

func legacySignature(path string) (string, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano()), nil
}

func (s *SQLiteStorage) checkLegacy() error {
	if !s.legacyBound {
		return nil
	}
	current, err := legacySignature(s.legacyPath)
	if err != nil {
		return err
	}
	if current != s.legacyStamp {
		return fmt.Errorf("legacy session changed after SQLite migration; refusing to discard divergent history")
	}
	return nil
}

func sqliteAppend(ctx context.Context, tx *sql.Tx, entry Entry, updateLeaf, normalize bool) error {
	if normalize && entry.ID == "" {
		entry.ID = newID("entry")
	}
	if normalize && entry.Timestamp == 0 {
		entry.Timestamp = time.Now().UnixMilli()
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO entries(id,parent_id,type,timestamp,payload) VALUES(?,?,?,?,?)", entry.ID, entry.ParentID, entry.Type, entry.Timestamp, payload); err != nil {
		return err
	}
	if updateLeaf {
		leaf := ""
		switch entry.Type {
		case EntryTypeLeaf:
			leaf = entry.TargetID
		case EntryTypeMessage, EntryTypeCompaction, EntryTypeMicroCompaction:
			leaf = entry.ID
		default:
			return nil
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO state(key,value) VALUES('leaf',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", leaf)
	}
	return err
}

func (s *SQLiteStorage) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStorage) Append(ctx context.Context, entry Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLegacy(); err != nil {
		return err
	}
	if entry.ID == "" {
		entry.ID = newID("entry")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = sqliteAppend(ctx, tx, entry, !s.forked, true); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if s.forked {
		switch entry.Type {
		case EntryTypeLeaf:
			s.forkLeaf = entry.TargetID
		case EntryTypeMessage, EntryTypeCompaction, EntryTypeMicroCompaction:
			s.forkLeaf = entry.ID
		}
	}
	return nil
}

func (s *SQLiteStorage) GetLeaf(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLegacy(); err != nil {
		return "", err
	}
	if s.forked {
		return s.forkLeaf, nil
	}
	var leaf string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM state WHERE key='leaf'").Scan(&leaf)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return leaf, err
}

func (s *SQLiteStorage) SetLeaf(ctx context.Context, id string) error {
	return s.Append(ctx, Entry{Type: EntryTypeLeaf, TargetID: id})
}

func (s *SQLiteStorage) GetPathToRoot(ctx context.Context, leaf string) ([]Entry, error) {
	if err := s.checkLegacy(); err != nil {
		return nil, err
	}
	if leaf == "" {
		var err error
		leaf, err = s.GetLeaf(ctx)
		if err != nil {
			return nil, err
		}
	}
	var path []Entry
	seen := make(map[string]bool)
	if leaf == "" {
		return path, nil
	}
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE history(id,parent_id,payload) AS (
		SELECT id,parent_id,payload FROM entries WHERE seq=(SELECT max(seq) FROM entries WHERE id=?)
		UNION ALL
		SELECT e.id,e.parent_id,e.payload FROM history h JOIN entries e
		ON e.seq=(SELECT max(seq) FROM entries WHERE id=h.parent_id) WHERE h.parent_id <> ''
	) SELECT payload FROM history`, leaf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	expected := leaf
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var entry Entry
		if err = json.Unmarshal(data, &entry); err != nil {
			return nil, err
		}
		if entry.ID != expected {
			return nil, fmt.Errorf("inconsistent session history at %q", expected)
		}
		if seen[entry.ID] {
			return nil, fmt.Errorf("cycle in session history at %q", entry.ID)
		}
		seen[entry.ID] = true
		path = append(path, entry)
		expected = entry.ParentID
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if expected != "" {
		return nil, fmt.Errorf("entry %q not found", expected)
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path, nil
}

func (s *SQLiteStorage) Fork(ctx context.Context, target string) (SessionStorage, error) {
	other := NewSQLiteStorage(s.path, s.legacyPath)
	if err := other.Init(); err != nil {
		return nil, err
	}
	other.forked, other.forkLeaf = true, target
	return other, nil
}

// Entries returns the complete durable log in write order, including entries
// outside the active branch. Callers use it for rebuildable search indexes.
func (s *SQLiteStorage) Entries(ctx context.Context) ([]Entry, error) {
	if err := s.checkLegacy(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM entries ORDER BY seq")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var e Entry
		if err = json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
