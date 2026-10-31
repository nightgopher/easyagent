package sessionmgr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/session"
	"github.com/stretchr/testify/require"
)

func searchFixture(t *testing.T, m *Manager, messages ...ai.Message) (string, *session.Session) {
	t.Helper()
	id, _, err := m.Create(context.Background())
	require.NoError(t, err)
	s, _, err := m.Open(context.Background(), id)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Storage().Close()) })
	for _, msg := range messages {
		require.NoError(t, s.AppendMessage(context.Background(), msg))
	}
	return id, s
}

func TestSearchDurableTextAndOriginalHistory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := NewManager(dir)
	id, s := searchFixture(t, m, ai.NewTextUserMessage("无关的开场"), ai.AssistantMessage{Text: "此前修复了迷你主机的访问令牌；调用 RefreshLiveSessions(ctx)。"}, ai.ToolResultMessage{Content: "tool-only-secret"}, ai.AssistantMessage{Thinking: "reasoning-only-secret", Text: "收尾"})
	ids, err := s.BuildContextEntryIDs(ctx)
	require.NoError(t, err)
	require.NoError(t, s.AppendCompaction(ctx, "压缩后的摘要"))
	for _, q := range []string{"迷你主机", "令牌", "牌", "refreshlivesessions(ctx)", "；调用"} {
		result, err := m.Search(ctx, SearchOptions{Query: q})
		require.NoError(t, err, q)
		require.Len(t, result.Hits, 1, q)
		require.Equal(t, id, result.Hits[0].SessionID)
		require.Equal(t, ids[1], result.Hits[0].EntryID)
		hit := result.Hits[0]
		require.Equal(t, strings.ToLower(q), strings.ToLower(string([]rune(hit.Snippet)[hit.MatchStart:hit.MatchEnd])))
	}
	for _, q := range []string{"tool-only-secret", "reasoning-only-secret", "压缩后的摘要"} {
		result, err := m.Search(ctx, SearchOptions{Query: q})
		require.NoError(t, err)
		require.Empty(t, result.Hits)
	}
	// A fresh Manager, with no opened sessions or runtime, reuses the disk index.
	m = NewManager(dir)
	result, err := m.Search(ctx, SearchOptions{Query: "令牌"})
	require.NoError(t, err)
	require.Equal(t, 1, result.Total)
	history, err := m.SearchHistory(ctx, id, ids[1])
	require.NoError(t, err)
	require.Len(t, history, 3)
	require.Equal(t, "无关的开场", history[0].Text)
	require.Equal(t, ids[1], history[1].EntryID)
	current, err := s.BuildContext(ctx)
	require.NoError(t, err)
	require.Len(t, current, 1, "history read must not move the live cursor")
	_, err = m.SearchHistory(ctx, "another-session", ids[1])
	require.ErrorIs(t, err, ErrHistoryNotFound)
}

func TestSearchReconcilesAppendRenameArchiveDeleteAndFilters(t *testing.T) {
	ctx := context.Background()
	m := NewManager(t.TempDir())
	id, s := searchFixture(t, m, ai.NewTextUserMessage("first"), ai.AssistantMessage{Text: "needle one"}, ai.AssistantMessage{Text: "needle two"})
	other, _ := searchFixture(t, m, ai.NewTextUserMessage("other"), ai.AssistantMessage{Text: "needle three"})
	result, err := m.Search(ctx, SearchOptions{Query: "needle", Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 3, result.Total)
	require.Len(t, result.Hits, 1)
	next, err := m.Search(ctx, SearchOptions{Query: "needle", Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.NotEqual(t, result.Hits[0].EntryID, next.Hits[0].EntryID)
	meta := sessionMetadata{}
	require.NoError(t, meta.write(filepath.Join(m.SessionsDir(), id), map[string]any{"title": "needle title", "workspace": "/project/a", "archived": true}))
	result, err = m.Search(ctx, SearchOptions{Query: "needle", Workspace: "/project/a", Archived: "true"})
	require.NoError(t, err)
	require.Equal(t, 3, result.Total)
	require.Equal(t, "title", result.Hits[0].Role, "title hits rank before body matches")
	result, err = m.Search(ctx, SearchOptions{Query: "needle", Archived: "false"})
	require.NoError(t, err)
	require.Equal(t, 1, result.Total)
	require.Equal(t, other, result.Hits[0].SessionID)
	result, err = m.Search(ctx, SearchOptions{Query: "needle", Since: time.Now().Add(time.Hour).UnixMilli()})
	require.NoError(t, err)
	require.Empty(t, result.Hits)
	require.NoError(t, s.AppendMessage(ctx, ai.AssistantMessage{Text: "newly appended"}))
	result, err = m.Search(ctx, SearchOptions{Query: "newly appended"})
	require.NoError(t, err)
	require.Equal(t, 1, result.Total)
	require.NoError(t, m.Delete(other))
	result, err = m.Search(ctx, SearchOptions{Query: "needle"})
	require.NoError(t, err)
	require.Equal(t, 3, result.Total)
	for _, hit := range result.Hits {
		require.Equal(t, id, hit.SessionID)
	}
}

func TestSearchPartialFileRepairAndTruncation(t *testing.T) {
	ctx := context.Background()
	m := NewManager(t.TempDir())
	id, _ := searchFixture(t, m, ai.NewTextUserMessage("valid message"))
	result, err := m.Search(ctx, SearchOptions{Query: "valid message"})
	require.NoError(t, err)
	require.NotEmpty(t, result.Hits)
	f, err := os.OpenFile(m.SessionPath(id), os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"id":"repair","type":"message","assistant":{"text":"repaired`)
	require.NoError(t, err)
	result, err = m.Search(ctx, SearchOptions{Query: "valid message"})
	require.NoError(t, err)
	require.Equal(t, 1, result.Unavailable)
	require.Empty(t, result.Hits)
	_, err = f.WriteString(" message\"}}\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	result, err = m.Search(ctx, SearchOptions{Query: "repaired"})
	require.NoError(t, err)
	require.Zero(t, result.Unavailable)
	require.Len(t, result.Hits, 1)
	require.NoError(t, os.WriteFile(m.SessionPath(id), []byte(""), 0600))
	result, err = m.Search(ctx, SearchOptions{Query: "repaired"})
	require.NoError(t, err)
	require.Empty(t, result.Hits)
}

func TestSearchLiteralUnicodeAndRetainedDeduplication(t *testing.T) {
	ctx := context.Background()
	m := NewManager(t.TempDir())
	text := strings.Repeat("前置内容", 20000) + `İSTANBUL "100%_done" 中文测试` + strings.Repeat("后", 100)
	id, s := searchFixture(t, m, ai.NewTextUserMessage("start"), ai.AssistantMessage{Text: text})
	history, err := s.BuildContext(ctx)
	require.NoError(t, err)
	require.NoError(t, s.AppendCompactionKeeping(ctx, "summary", history[1:], nil))
	for _, q := range []string{`"100%_done"`, "İstanbul", "中文测试"} {
		result, err := m.Search(ctx, SearchOptions{Query: q})
		require.NoError(t, err)
		require.Len(t, result.Hits, 1)
		hit := result.Hits[0]
		require.Less(t, len([]rune(hit.Snippet)), 200)
		require.Equal(t, strings.ToLower(q), strings.ToLower(string([]rune(hit.Snippet)[hit.MatchStart:hit.MatchEnd])))
	}
	// Rewriting a session replaces its indexed documents, even at the same size.
	raw, err := os.ReadFile(m.SessionPath(id))
	require.NoError(t, err)
	raw = []byte(strings.ReplaceAll(string(raw), "中文测试", "替换内容"))
	require.NoError(t, os.WriteFile(m.SessionPath(id), raw, 0600))
	result, err := m.Search(ctx, SearchOptions{Query: "中文测试"})
	require.NoError(t, err)
	require.Empty(t, result.Hits)
	result, err = m.Search(ctx, SearchOptions{Query: "替换内容"})
	require.NoError(t, err)
	require.Len(t, result.Hits, 1)
}

func TestSearchValidationCancellationAndConcurrency(t *testing.T) {
	m := NewManager(t.TempDir())
	searchFixture(t, m, ai.NewTextUserMessage("concurrent"))
	for _, opts := range []SearchOptions{{}, {Query: "\x00"}, {Query: "\xff"}, {Query: strings.Repeat("中", 257)}, {Query: "x", Archived: "invalid"}, {Query: "x", Offset: -1}, {Query: "x", Limit: -1}, {Query: "x", Limit: 101}} {
		_, err := m.Search(context.Background(), opts)
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.Search(ctx, SearchOptions{Query: "concurrent"})
	require.ErrorIs(t, err, context.Canceled)
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			result, err := m.Search(context.Background(), SearchOptions{Query: "concurrent"})
			if err != nil || result.Total != 2 {
				t.Errorf("search: %+v %v", result, err)
			}
		})
	}
	wg.Wait()
}

func TestSearchBackfillCheckpointsAndDeletion(t *testing.T) {
	ctx := context.Background()
	m := NewManager(t.TempDir())
	var ids []string
	// Cross two backfill transaction boundaries, then reconcile removals and
	// metadata changes through the persisted index of a new Manager.
	for range 65 {
		id, _, err := m.Create(ctx)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(m.SessionPath(id), []byte(`{"id":"answer","type":"message","assistant":{"text":"checkpoint needle"}}`+"\n"), 0600))
		ids = append(ids, id)
	}
	result, err := m.Search(ctx, SearchOptions{Query: "needle", Limit: 100})
	require.NoError(t, err)
	require.Equal(t, 65, result.Total)
	require.Len(t, result.Hits, 65)
	for _, id := range ids[:33] {
		require.NoError(t, m.Delete(id))
	}
	for _, id := range ids[33:] {
		require.NoError(t, m.SaveMeta(id, "/after-backfill", "coding"))
	}
	m = NewManager(m.dataDir)
	result, err = m.Search(ctx, SearchOptions{Query: "needle", Workspace: "/after-backfill", Limit: 100})
	require.NoError(t, err)
	require.Equal(t, 32, result.Total)
	require.Zero(t, result.Unavailable)
	_, err = m.SearchHistory(ctx, ids[0], "answer")
	require.ErrorIs(t, err, ErrHistoryNotFound)
}

func BenchmarkSearchWarmHistory(b *testing.B) {
	m := NewManager(b.TempDir())
	ctx := context.Background()
	for i := range 1000 {
		id, _, err := m.Create(ctx)
		if err != nil {
			b.Fatal(err)
		}
		entry := map[string]any{"id": "answer", "type": "message", "assistant": map[string]any{"text": strings.Repeat("stored conversation body ", 50) + "中文关键词"}}
		data, _ := json.Marshal(entry)
		if err := os.WriteFile(m.SessionPath(id), append(data, '\n'), 0600); err != nil {
			b.Fatal(i, err)
		}
	}
	if _, err := m.Search(ctx, SearchOptions{Query: "中文关键词"}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Search(ctx, SearchOptions{Query: "中文关键词"}); err != nil {
			b.Fatal(err)
		}
	}
}
