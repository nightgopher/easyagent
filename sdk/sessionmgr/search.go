package sessionmgr

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// SearchOptions searches literal text, ignoring case, across durable user and
// assistant messages. Empty Archived includes both active and archived sessions.
type SearchOptions struct {
	Query, Workspace, Archived string
	Since                      int64 // session activity, Unix milliseconds
	Limit, Offset              int
}

type SearchHit struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Workspace string `json:"workspace"`
	Archived  bool   `json:"archived"`
	UpdatedAt int64  `json:"updated_at"`
	EntryID   string `json:"entry_id"`
	Role      string `json:"role"`
	Timestamp int64  `json:"timestamp"`
	Snippet   string `json:"snippet"`
	// Match offsets are rune offsets within Snippet, not byte offsets.
	MatchStart int `json:"match_start"`
	MatchEnd   int `json:"match_end"`
}

type SearchResults struct {
	Hits        []SearchHit `json:"hits"`
	Total       int         `json:"total"`
	Unavailable int         `json:"unavailable_sessions"`
}

type HistoryMessage struct {
	EntryID   string `json:"entry_id"`
	Role      string `json:"role"`
	Timestamp int64  `json:"timestamp"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

var ErrHistoryNotFound = errors.New("history message not found")

// Search uses a rebuildable, host-local SQLite index. JSONL remains the source
// of truth. Unchanged transcripts are never read on subsequent searches; changed
// sessions are replaced atomically, including metadata-only changes and deletes.
func (m *Manager) Search(ctx context.Context, opts SearchOptions) (SearchResults, error) {
	result := SearchResults{Hits: []SearchHit{}}
	opts.Query = strings.TrimSpace(opts.Query)
	if opts.Query == "" || !utf8.ValidString(opts.Query) || strings.ContainsRune(opts.Query, 0) || utf8.RuneCountInString(opts.Query) > 256 {
		return result, errors.New("query must contain 1 to 256 characters")
	}
	if opts.Archived != "" && opts.Archived != "all" && opts.Archived != "true" && opts.Archived != "false" {
		return result, errors.New("invalid archived filter")
	}
	if opts.Limit == 0 {
		opts.Limit = 30
	}
	if opts.Limit < 0 || opts.Limit > 100 || opts.Offset < 0 || opts.Offset > 100000 || opts.Since < 0 {
		return result, errors.New("invalid search bounds")
	}
	m.searchMu.Lock()
	defer m.searchMu.Unlock()
	db, err := m.searchDB(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = db.Close() }()
	result.Unavailable, err = m.syncSearch(ctx, db)
	if err != nil {
		return result, err
	}

	needle := strings.Map(unicode.ToLower, opts.Query)
	where := "s.available = 1 AND instr(d.search_text, ?) > 0"
	args := []any{needle}
	// Trigram MATCH handles Chinese, punctuation and code without a word
	// segmenter. One/two-character queries use a literal scan of cached text.
	if utf8.RuneCountInString(needle) >= 3 {
		where += " AND d.id IN (SELECT rowid FROM search_fts WHERE search_fts MATCH ?)"
		args = append(args, `"`+strings.ReplaceAll(needle, `"`, `""`)+`"`)
	}
	if opts.Workspace != "" {
		where += " AND s.workspace = ?"
		args = append(args, opts.Workspace)
	}
	if opts.Archived == "true" || opts.Archived == "false" {
		where += " AND s.archived = ?"
		args = append(args, opts.Archived == "true")
	}
	if opts.Since > 0 {
		where += " AND s.updated_at >= ?"
		args = append(args, opts.Since)
	}
	from := " FROM search_documents d JOIN search_sessions s ON s.id = d.session_id WHERE " + where
	if err = db.QueryRowContext(ctx, "SELECT count(*)"+from, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := db.QueryContext(ctx, `SELECT s.id, s.title, s.workspace, s.archived, s.updated_at,
		d.entry_id, d.role, d.timestamp, d.body`+from+`
		ORDER BY (d.role = 'title') DESC, s.updated_at DESC, s.id, d.timestamp DESC, d.id DESC LIMIT ? OFFSET ?`, append(args, opts.Limit, opts.Offset)...)
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var hit SearchHit
		var body string
		if err = rows.Scan(&hit.SessionID, &hit.Title, &hit.Workspace, &hit.Archived, &hit.UpdatedAt, &hit.EntryID, &hit.Role, &hit.Timestamp, &body); err != nil {
			return result, err
		}
		hit.Snippet, hit.MatchStart, hit.MatchEnd = searchSnippet(body, needle)
		result.Hits = append(result.Hits, hit)
	}
	return result, rows.Err()
}

// SearchHistory returns the original matched message with two messages on each
// side, even if compaction or branch navigation removed it from model context.
// It never loads, moves, or rewrites the live session's cursor.
func (m *Manager) SearchHistory(ctx context.Context, sessionID, entryID string) ([]HistoryMessage, error) {
	m.searchMu.Lock()
	defer m.searchMu.Unlock()
	db, err := m.searchDB(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	if _, err = m.syncSearch(ctx, db); err != nil {
		return nil, err
	}
	var anchor int64
	err = db.QueryRowContext(ctx, `SELECT d.id FROM search_documents d JOIN search_sessions s ON s.id = d.session_id
		WHERE s.available = 1 AND d.session_id = ? AND d.entry_id = ? AND d.role != 'title'`, sessionID, entryID).Scan(&anchor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrHistoryNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT entry_id, role, timestamp, body FROM search_documents WHERE id IN (
		SELECT id FROM (SELECT id FROM search_documents WHERE session_id = ? AND role != 'title' AND id < ? ORDER BY id DESC LIMIT 2)
		UNION ALL SELECT id FROM (SELECT id FROM search_documents WHERE session_id = ? AND role != 'title' AND id >= ? ORDER BY id LIMIT 3)
	) ORDER BY id`, sessionID, anchor, sessionID, anchor)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var messages []HistoryMessage
	for rows.Next() {
		var msg HistoryMessage
		if err = rows.Scan(&msg.EntryID, &msg.Role, &msg.Timestamp, &msg.Text); err != nil {
			return nil, err
		}
		// Keep the matched message complete; surrounding context is bounded.
		if msg.EntryID != entryID && utf8.RuneCountInString(msg.Text) > 4000 {
			msg.Text = string([]rune(msg.Text)[:4000])
			msg.Truncated = true
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

const searchSchema = `
CREATE TABLE IF NOT EXISTS search_sessions (
 id TEXT PRIMARY KEY, title TEXT NOT NULL, workspace TEXT NOT NULL, archived INTEGER NOT NULL,
 updated_at INTEGER NOT NULL, stamp TEXT NOT NULL, available INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS search_documents (
 id INTEGER PRIMARY KEY, session_id TEXT NOT NULL, entry_id TEXT NOT NULL, role TEXT NOT NULL,
 timestamp INTEGER NOT NULL, body TEXT NOT NULL, search_text TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS search_session_documents ON search_documents(session_id, entry_id);
CREATE VIRTUAL TABLE IF NOT EXISTS search_fts USING fts5(search_text, content='search_documents', content_rowid='id', tokenize='trigram case_sensitive 1');
CREATE TRIGGER IF NOT EXISTS search_insert AFTER INSERT ON search_documents BEGIN
 INSERT INTO search_fts(rowid, search_text) VALUES (new.id, new.search_text);
END;
CREATE TRIGGER IF NOT EXISTS search_delete AFTER DELETE ON search_documents BEGIN
 INSERT INTO search_fts(search_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
END;
`

func (m *Manager) searchDB(ctx context.Context) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(m.dataDir, "session-search")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "index-v1.sqlite")
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile("index-v1.sqlite", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout=5000;"+searchSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

type searchStamp struct {
	Stamp     string
	Available bool
}

func (m *Manager) syncSearch(ctx context.Context, db *sql.DB) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, "SELECT id, stamp, available FROM search_sessions")
	if err != nil {
		return 0, err
	}
	known := map[string]searchStamp{}
	for rows.Next() {
		var id string
		var stamp searchStamp
		if err = rows.Scan(&id, &stamp.Stamp, &stamp.Available); err != nil {
			_ = rows.Close()
			return 0, err
		}
		known[id] = stamp
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(m.SessionsDir())
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	unavailable, changed := 0, 0
	for _, dir := range entries {
		if err = ctx.Err(); err != nil {
			return 0, err
		}
		if !dir.IsDir() {
			continue
		}
		id := dir.Name()
		previous := known[id]
		delete(known, id)
		stamp, stampErr := m.transcriptStamp(id)
		if stampErr == nil && stamp == previous.Stamp && previous.Available {
			continue
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM search_documents WHERE session_id = ?", id); err != nil {
			return 0, err
		}
		if stampErr == nil {
			stampErr = m.indexTranscript(ctx, tx, id, stamp)
		}
		if stampErr != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			// Do not serve stale matches when a transcript is unreadable. Retry it
			// next time, and tell clients that the results are incomplete.
			unavailable++
			if _, err = tx.ExecContext(ctx, "DELETE FROM search_documents WHERE session_id = ?", id); err != nil {
				return 0, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO search_sessions VALUES (?, '', '', 0, 0, '', 0)`, id); err != nil {
				return 0, err
			}
		}
		// Checkpoint backfill so a cancelled keystroke/HTTP timeout does not
		// repeatedly discard all progress on a large cold archive. A session's
		// documents and stamp always commit together.
		changed++
		if changed%32 == 0 {
			if err = tx.Commit(); err != nil {
				return 0, err
			}
			next, err := db.BeginTx(ctx, nil)
			if err != nil {
				return 0, err
			}
			tx = next
		}
	}
	for id := range known {
		if _, err = tx.ExecContext(ctx, "DELETE FROM search_documents WHERE session_id = ?", id); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM search_sessions WHERE id = ?", id); err != nil {
			return 0, err
		}
	}
	return unavailable, tx.Commit()
}

func (m *Manager) transcriptStamp(id string) (string, error) {
	info, err := os.Lstat(m.SessionPath(id))
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular transcript")
	}
	stamp := fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
	meta, err := os.Lstat(filepath.Join(m.SessionsDir(), id, "meta.json"))
	if os.IsNotExist(err) {
		return stamp + "/no-meta", nil
	}
	if err != nil {
		return "", err
	}
	if !meta.Mode().IsRegular() {
		return "", errors.New("not regular metadata")
	}
	return fmt.Sprintf("%s/%d/%d", stamp, meta.Size(), meta.ModTime().UnixNano()), nil
}

// Only decode searchable fields: image payloads, tool results, and reasoning
// never enter the index, and large base64 attachments are not decoded to bytes.
type searchEntry struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	User      *struct {
		DisplayText string `json:"display_text"`
		Content     []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"user"`
	Assistant *struct {
		Text string `json:"text"`
	} `json:"assistant"`
	Retained []searchEntry `json:"retained"`
}

func (m *Manager) indexTranscript(ctx context.Context, tx *sql.Tx, id, stamp string) error {
	meta, err := readMetadata(filepath.Join(m.SessionsDir(), id))
	if err != nil {
		return err
	}
	var title, workspace string
	var archived bool
	if err = meta.decodeField("title", &title); err != nil {
		return err
	}
	if err = meta.decodeField("workspace", &workspace); err != nil {
		return err
	}
	if err = meta.decodeField("archived", &archived); err != nil {
		return err
	}
	f, err := os.Open(m.SessionPath(id))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	updated := info.ModTime().UnixMilli()
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO search_documents(session_id, entry_id, role, timestamp, body, search_text) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	seen := map[string]bool{}
	var add func(searchEntry) error
	add = func(entry searchEntry) error {
		if entry.Type == "compaction" {
			for _, kept := range entry.Retained {
				if err := add(kept); err != nil {
					return err
				}
			}
			return nil
		}
		if entry.Type != "message" || entry.ID == "" || seen[entry.ID] {
			return nil
		}
		seen[entry.ID] = true
		var body, role string
		if entry.User != nil {
			role = "user"
			body = entry.User.DisplayText
			if body == "" {
				var blocks []string
				for _, block := range entry.User.Content {
					if block.Type == "text" {
						blocks = append(blocks, block.Text)
					}
				}
				body = strings.Join(blocks, "\n")
			}
			if title == "" {
				title = strings.SplitN(body, "\n", 2)[0]
			}
		} else if entry.Assistant != nil {
			role = "assistant"
			body = entry.Assistant.Text
		}
		if body == "" {
			return nil
		}
		updated = max(updated, entry.Timestamp)
		_, err := stmt.ExecContext(ctx, id, entry.ID, role, entry.Timestamp, body, strings.Map(unicode.ToLower, body))
		return err
	}
	scanner := bufio.NewScanner(io.NewSectionReader(f, 0, info.Size()))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if err = ctx.Err(); err != nil {
			return err
		}
		var entry searchEntry
		if err = json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return err
		}
		if err = add(entry); err != nil {
			return err
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	// If the file changed while reading, use this consistent snapshot now and
	// force a refresh next query instead of declaring the newer file indexed.
	if current, err := m.transcriptStamp(id); err != nil || current != stamp {
		stamp = ""
	}
	if title == "" {
		title = id
	}
	if _, err = stmt.ExecContext(ctx, id, "", "title", updated, title, strings.Map(unicode.ToLower, title)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO search_sessions VALUES (?, ?, ?, ?, ?, ?, 1)", id, title, workspace, archived, updated, stamp)
	return err
}

func searchSnippet(body, needle string) (string, int, int) {
	text := []rune(body)
	folded := strings.Map(unicode.ToLower, body)
	at := strings.Index(folded, needle)
	if at < 0 {
		return "", 0, 0
	}
	start := utf8.RuneCountInString(folded[:at])
	end := start + utf8.RuneCountInString(needle)
	left, right := max(0, start-40), min(len(text), max(end+80, start+140))
	snippet := string(text[left:right])
	start -= left
	end -= left
	if left > 0 {
		snippet = "…" + snippet
		start++
		end++
	}
	if right < len(text) {
		snippet += "…"
	}
	return snippet, start, end
}
