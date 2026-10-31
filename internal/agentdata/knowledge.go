package agentdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type KnowledgeBinding struct {
	SourceID string `json:"source_id"`
	Access   string `json:"access"` // read or write (write also permits read)
}

type KnowledgeSource struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Directories []string `json:"directories"` // relative directories; default "."
	Writable    bool     `json:"writable"`
	Disabled    bool     `json:"disabled"`
	Version     int64    `json:"version"`
	UpdatedAt   string   `json:"updated_at"`
}

type KnowledgeHit struct {
	SourceID   string `json:"source_id"`
	SourceName string `json:"source_name"`
	Path       string `json:"path"`
	Title      string `json:"title"`
	Snippet    string `json:"snippet"`
	Modified   string `json:"modified"`
}

const knowledgeSchema = `
CREATE TABLE IF NOT EXISTS knowledge_sources(id TEXT PRIMARY KEY, definition TEXT NOT NULL, version INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS knowledge_documents(
 id INTEGER PRIMARY KEY, source_id TEXT NOT NULL, path TEXT NOT NULL,
 title TEXT NOT NULL, body TEXT NOT NULL, stamp TEXT NOT NULL, modified TEXT NOT NULL,
 UNIQUE(source_id,path));
CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_fts USING fts5(title, body, content='knowledge_documents', content_rowid='id', tokenize='trigram');
CREATE TRIGGER IF NOT EXISTS knowledge_insert AFTER INSERT ON knowledge_documents BEGIN
 INSERT INTO knowledge_fts(rowid,title,body) VALUES(new.id,new.title,new.body); END;
CREATE TRIGGER IF NOT EXISTS knowledge_delete AFTER DELETE ON knowledge_documents BEGIN
 INSERT INTO knowledge_fts(knowledge_fts,rowid,title,body) VALUES('delete',old.id,old.title,old.body); END;
CREATE TRIGGER IF NOT EXISTS knowledge_update AFTER UPDATE ON knowledge_documents BEGIN
 INSERT INTO knowledge_fts(knowledge_fts,rowid,title,body) VALUES('delete',old.id,old.title,old.body);
 INSERT INTO knowledge_fts(rowid,title,body) VALUES(new.id,new.title,new.body); END;
`

// EnsureLegacySource registers existing personal knowledge without moving files
// or granting new agents access. User edits to this entry are never overwritten.
func (s *Store) EnsureLegacySource(ctx context.Context, root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	source := KnowledgeSource{ID: "legacy-agent-lessons", Name: "agent-lessons", Path: abs, Directories: []string{"knowledge", "issues", "docs"}, Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	data, err := json.Marshal(source)
	if err != nil {
		return err
	}
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(ctx, "INSERT OR IGNORE INTO knowledge_sources(id,definition,version) VALUES(?,?,1)", source.ID, string(data))
	return err
}

func normalizeSource(source *KnowledgeSource) error {
	source.Name = strings.TrimSpace(source.Name)
	if source.Name == "" || len([]rune(source.Name)) > 100 {
		return fmt.Errorf("source name must contain 1–100 characters")
	}
	if !filepath.IsAbs(source.Path) {
		return fmt.Errorf("source path must be absolute on this host")
	}
	source.Path = filepath.Clean(source.Path)
	if len(source.Directories) == 0 {
		source.Directories = []string{"."}
	}
	if len(source.Directories) > 32 {
		return fmt.Errorf("too many source directories")
	}
	for _, dir := range source.Directories {
		if !fs.ValidPath(dir) || strings.Contains(dir, "\\") {
			return fmt.Errorf("source directories must be relative paths")
		}
	}
	return nil
}

func (s *Store) SaveSource(ctx context.Context, source KnowledgeSource) (KnowledgeSource, error) {
	if err := normalizeSource(&source); err != nil {
		return KnowledgeSource{}, err
	}
	create := source.ID == ""
	if create {
		source.ID, source.Version = "kb-"+uuid.NewString(), 0
	}
	previous := source.Version
	source.Version++
	source.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(source)
	if err != nil {
		return KnowledgeSource{}, err
	}
	db, err := s.open(ctx)
	if err != nil {
		return KnowledgeSource{}, err
	}
	defer func() { _ = db.Close() }()
	if create {
		_, err = db.ExecContext(ctx, "INSERT INTO knowledge_sources(id,definition,version) VALUES(?,?,?)", source.ID, string(data), source.Version)
	} else {
		var result sql.Result
		result, err = db.ExecContext(ctx, "UPDATE knowledge_sources SET definition=?,version=? WHERE id=? AND version=?", string(data), source.Version, source.ID, previous)
		if err == nil {
			var count int64
			count, err = result.RowsAffected()
			if err == nil && count == 0 {
				err = ErrConflict
			}
		}
	}
	return source, err
}

func (s *Store) GetSource(ctx context.Context, id string) (KnowledgeSource, error) {
	db, err := s.open(ctx)
	if err != nil {
		return KnowledgeSource{}, err
	}
	defer func() { _ = db.Close() }()
	var data string
	if err := db.QueryRowContext(ctx, "SELECT definition FROM knowledge_sources WHERE id=?", id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return KnowledgeSource{}, ErrNotFound
		}
		return KnowledgeSource{}, err
	}
	var source KnowledgeSource
	err = json.Unmarshal([]byte(data), &source)
	return source, err
}

func (s *Store) ListSources(ctx context.Context) ([]KnowledgeSource, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, "SELECT definition FROM knowledge_sources ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []KnowledgeSource{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var source KnowledgeSource
		if err := json.Unmarshal([]byte(data), &source); err != nil {
			return nil, err
		}
		result = append(result, source)
	}
	return result, rows.Err()
}

// AgentSource checks both the binding and the source policy on every access.
func (s *Store) AgentSource(ctx context.Context, agentID, sourceID string, write bool) (KnowledgeSource, error) {
	a, err := s.GetAgent(ctx, agentID)
	if err != nil {
		return KnowledgeSource{}, err
	}
	if a.Disabled {
		return KnowledgeSource{}, ErrDisabled
	}
	for _, binding := range a.Knowledge {
		if binding.SourceID != sourceID {
			continue
		}
		source, err := s.GetSource(ctx, sourceID)
		if err != nil {
			return KnowledgeSource{}, err
		}
		if source.Disabled || write && (binding.Access != "write" || !source.Writable) {
			return KnowledgeSource{}, fmt.Errorf("knowledge source access is not granted")
		}
		return source, nil
	}
	return KnowledgeSource{}, fmt.Errorf("knowledge source is not bound to this agent")
}

func documentAllowed(source KnowledgeSource, name string) bool {
	if !fs.ValidPath(name) || strings.Contains(name, "\\") || !strings.EqualFold(path.Ext(name), ".md") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || slices.Contains([]string{"node_modules", "_trash", "chatgpt-export", "doubao-export", "gemini-export"}, part) {
			return false
		}
	}
	if slices.Contains([]string{"agents.md", "claude.md", "index.md"}, strings.ToLower(path.Base(name))) {
		return false
	}
	for _, dir := range source.Directories {
		if dir == "." || strings.HasPrefix(name, dir+"/") {
			return true
		}
	}
	return false
}

const maxKnowledgeBytes = 1024 * 1024

func readDocument(root *os.Root, name string) (string, error) {
	for i, parts := 0, strings.Split(name, "/"); i < len(parts); i++ {
		info, err := root.Lstat(filepath.Join(parts[:i+1]...))
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("knowledge symlinks are not followed")
		}
	}
	f, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxKnowledgeBytes {
		return "", fmt.Errorf("knowledge file must be Markdown text up to 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKnowledgeBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxKnowledgeBytes || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", fmt.Errorf("knowledge file is not bounded UTF-8 text")
	}
	return string(data), nil
}

func (s *Store) ReadKnowledge(ctx context.Context, sourceID, name string) (string, error) {
	source, err := s.GetSource(ctx, sourceID)
	if err != nil {
		return "", err
	}
	if source.Disabled || !documentAllowed(source, name) {
		return "", fmt.Errorf("knowledge document is outside the selected source")
	}
	root, err := os.OpenRoot(source.Path)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	return readDocument(root, name)
}

// IndexSource updates only changed Markdown documents in a single transaction.
// A failed scan rolls back, and callers surface the error instead of serving a
// stale index from a missing directory. Files, not these rows, are authoritative.
func (s *Store) IndexSource(ctx context.Context, sourceID string) error {
	source, err := s.GetSource(ctx, sourceID)
	if err != nil {
		return err
	}
	if source.Disabled {
		return fmt.Errorf("knowledge source is disabled")
	}
	root, err := os.OpenRoot(source.Path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, "SELECT path,stamp FROM knowledge_documents WHERE source_id=?", sourceID)
	if err != nil {
		return err
	}
	known := map[string]string{}
	for rows.Next() {
		var name, stamp string
		if err := rows.Scan(&name, &stamp); err != nil {
			_ = rows.Close()
			return err
		}
		known[name] = stamp
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if name != "." && (strings.HasPrefix(d.Name(), ".") || slices.Contains([]string{"node_modules", "_trash", "chatgpt-export", "doubao-export", "gemini-export"}, d.Name())) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !documentAllowed(source, name) {
			return nil
		}
		if len(seen) >= 20000 {
			return fmt.Errorf("knowledge source exceeds 20000 documents; select narrower directories")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxKnowledgeBytes {
			return nil
		}
		stamp := fmt.Sprintf("%d:%d:%s", info.Size(), info.ModTime().UnixNano(), source.Path)
		seen[name] = true
		if known[name] == stamp {
			return nil
		}
		body, err := readDocument(root, name)
		if err != nil {
			return err
		}
		title := path.Base(name)
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "# ") {
				title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
				break
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO knowledge_documents(source_id,path,title,body,stamp,modified) VALUES(?,?,?,?,?,?) ON CONFLICT(source_id,path) DO UPDATE SET title=excluded.title,body=excluded.body,stamp=excluded.stamp,modified=excluded.modified`, sourceID, name, title, body, stamp, info.ModTime().UTC().Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		return err
	}
	for name := range known {
		if !seen[name] {
			if _, err := tx.ExecContext(ctx, "DELETE FROM knowledge_documents WHERE source_id=? AND path=?", sourceID, name); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) SearchKnowledge(ctx context.Context, sourceID, query string, limit int) ([]KnowledgeHit, error) {
	if len(query) > 512 {
		return nil, fmt.Errorf("query is too long")
	}
	if err := s.IndexSource(ctx, sourceID); err != nil {
		return nil, err
	}
	source, err := s.GetSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	if limit < 1 || limit > 50 {
		limit = 10
	}
	query = strings.TrimSpace(query)
	statement := `SELECT path,title,substr(body,1,700),modified FROM knowledge_documents WHERE source_id=?`
	args := []any{sourceID}
	order := " ORDER BY modified DESC,path"

	terms := strings.Fields(query)
	useFTS := len(terms) > 0
	for _, term := range terms {
		if utf8.RuneCountInString(term) < 3 {
			useFTS = false
		}
	}
	if useFTS {
		statement = `SELECT d.path,d.title,snippet(knowledge_fts,1,'','',' … ',48),d.modified FROM knowledge_fts JOIN knowledge_documents d ON d.id=knowledge_fts.rowid WHERE d.source_id=? AND knowledge_fts MATCH ?`
		quoted := make([]string, 0, len(terms))
		for _, term := range terms {
			quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
		}
		args = append(args, strings.Join(quoted, " AND "))
		order = " ORDER BY rank,d.path"
	} else {
		for _, term := range terms {
			statement += " AND instr(lower(title || ' ' || body),lower(?))>0"
			args = append(args, term)
		}
	}
	statement += order + " LIMIT ?"
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	hits := []KnowledgeHit{}
	for rows.Next() {
		hit := KnowledgeHit{SourceID: sourceID, SourceName: source.Name}
		if err := rows.Scan(&hit.Path, &hit.Title, &hit.Snippet, &hit.Modified); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

// SearchAgentKnowledge bounds both source scope and total context returned.
func (s *Store) SearchAgentKnowledge(ctx context.Context, agentID, query string) ([]KnowledgeHit, error) {
	a, err := s.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if a.Disabled {
		return nil, ErrDisabled
	}
	hits := []KnowledgeHit{}
	for _, binding := range a.Knowledge {
		if _, err := s.AgentSource(ctx, agentID, binding.SourceID, false); err != nil {
			return nil, err
		}
		found, err := s.SearchKnowledge(ctx, binding.SourceID, query, 5)
		if err != nil {
			return nil, err
		}
		hits = append(hits, found...)
		if len(hits) >= 20 {
			return hits[:20], nil
		}
	}
	return hits, nil
}
