package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/stretchr/testify/require"
)

func TestSQLiteCommittedDataSurvivesAbruptExit(t *testing.T) {
	if path := os.Getenv("EASYAGENT_TEST_SQLITE_CRASH_PATH"); path != "" {
		store := NewSQLiteStorage(path, "")
		if store.Init() != nil || store.Append(context.Background(), Entry{ID: "committed", Type: EntryTypeMessage, User: &ai.UserMessage{Content: []ai.ContentBlock{{Type: "text", Text: "durable"}}}}) != nil {
			os.Exit(2)
		}
		os.Exit(0) // Simulate a process exit without closing the database.
	}
	path := filepath.Join(t.TempDir(), "session.sqlite")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSQLiteCommittedDataSurvivesAbruptExit$")
	cmd.Env = append(os.Environ(), "EASYAGENT_TEST_SQLITE_CRASH_PATH="+path)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	store := NewSQLiteStorage(path, "")
	require.NoError(t, store.Init())
	defer store.Close()
	entries, err := store.GetPathToRoot(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "durable", entries[0].User.Content[0].Text)
}

func TestSQLiteDetectsBrokenHistory(t *testing.T) {
	store := NewSQLiteStorage(filepath.Join(t.TempDir(), "session.sqlite"), "")
	require.NoError(t, store.Init())
	defer store.Close()
	ctx := context.Background()
	require.NoError(t, store.Append(ctx, Entry{ID: "missing-parent", ParentID: "absent", Type: EntryTypeMessage}))
	_, err := store.GetPathToRoot(ctx, "")
	require.ErrorContains(t, err, "not found")
	require.NoError(t, store.Append(ctx, Entry{ID: "cycle", ParentID: "cycle", Type: EntryTypeMessage}))
	_, err = store.GetPathToRoot(ctx, "")
	require.ErrorContains(t, err, "cycle")
}

func TestSQLiteForkDoesNotMoveParentCursor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session.sqlite")
	store := NewSQLiteStorage(path, "")
	require.NoError(t, store.Init())
	require.NoError(t, store.Append(ctx, Entry{ID: "one", Type: EntryTypeMessage}))
	require.NoError(t, store.Append(ctx, Entry{ID: "two", ParentID: "one", Type: EntryTypeMessage}))
	fork, err := store.Fork(ctx, "one")
	require.NoError(t, err)
	require.NoError(t, fork.Append(ctx, Entry{ID: "branch", ParentID: "one", Type: EntryTypeMessage}))
	parentLeaf, err := store.GetLeaf(ctx)
	require.NoError(t, err)
	require.Equal(t, "two", parentLeaf)
	forkLeaf, err := fork.GetLeaf(ctx)
	require.NoError(t, err)
	require.Equal(t, "branch", forkLeaf)
	require.NoError(t, fork.Close())
	require.NoError(t, store.Close())
	store = NewSQLiteStorage(path, "")
	require.NoError(t, store.Init())
	defer store.Close()
	parentLeaf, err = store.GetLeaf(ctx)
	require.NoError(t, err)
	require.Equal(t, "two", parentLeaf)
}

func TestSQLiteSessionRestoresHistoryAndBranch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session.sqlite")
	store := NewSQLiteStorage(path, "")
	require.NoError(t, store.Init())
	s := New(store)
	require.NoError(t, s.AppendMessage(ctx, ai.NewTextUserMessage("question")))
	first, err := store.GetLeaf(ctx)
	require.NoError(t, err)
	require.NoError(t, s.AppendMessage(ctx, ai.AssistantMessage{Text: "answer"}))
	require.NoError(t, s.MoveTo(ctx, first, ""))
	require.NoError(t, s.AppendMessage(ctx, ai.AssistantMessage{Text: "new answer"}))
	require.NoError(t, store.Close())
	store = NewSQLiteStorage(path, "")
	require.NoError(t, store.Init())
	defer store.Close()
	s = New(store)
	require.NoError(t, s.InitFromStorage(ctx))
	messages, err := s.BuildContext(ctx)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Equal(t, "new answer", messages[1].(ai.AssistantMessage).Text)
	entries, err := store.Entries(ctx)
	require.NoError(t, err)
	require.Greater(t, len(entries), 3)
}

func TestSQLiteUsesLiteralSpecialCharactersInPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "项目 ?#% space")
	path := filepath.Join(dir, "session.sqlite")
	store := NewSQLiteStorage(path, "")
	require.NoError(t, store.Init())
	require.NoError(t, store.Append(context.Background(), Entry{ID: "one", Type: EntryTypeMessage, User: &ai.UserMessage{Content: []ai.ContentBlock{{Type: "text", Text: "preserved"}}}}))
	require.NoError(t, store.Close())
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
	store = NewSQLiteStorage(path, "")
	require.NoError(t, store.Init())
	defer store.Close()
	entries, err := store.GetPathToRoot(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "preserved", entries[0].User.Content[0].Text)
}

func TestSQLiteSupportsRecordsAboveLegacyLimit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session.sqlite")
	store := NewSQLiteStorage(path, "")
	require.NoError(t, store.Init())
	want := strings.Repeat("x", 17*1024*1024)
	require.NoError(t, store.Append(ctx, Entry{Type: EntryTypeMessage, Assistant: &ai.AssistantMessage{Text: want}}))
	require.NoError(t, store.Close())
	store = NewSQLiteStorage(path, "")
	require.NoError(t, store.Init())
	defer store.Close()
	entries, err := store.GetPathToRoot(ctx, "")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, want, entries[0].Assistant.Text)
}

func TestSQLiteRejectsDivergentLegacyAfterMigration(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "session.jsonl")
	db := filepath.Join(dir, "session.sqlite")
	require.NoError(t, os.WriteFile(legacy, []byte("{\"id\":\"one\",\"type\":\"message\"}\n"), 0600))
	store := NewSQLiteStorage(db, legacy)
	require.NoError(t, store.Init())
	f, err := os.OpenFile(legacy, os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = f.WriteString("{\"id\":\"two\",\"type\":\"message\"}\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.ErrorContains(t, store.Append(context.Background(), Entry{Type: EntryTypeMessage}), "legacy session changed")
	_, err = store.GetLeaf(context.Background())
	require.ErrorContains(t, err, "legacy session changed")
	require.NoError(t, store.Close())
	store = NewSQLiteStorage(db, legacy)
	require.ErrorContains(t, store.Init(), "legacy session changed")
}

func TestSQLiteMigrationIsAtomicAndPreservesLegacy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	legacy := filepath.Join(dir, "session.jsonl")
	db := filepath.Join(dir, "session.sqlite")
	original := []byte("{\"id\":\"one\",\"type\":\"message\",\"user\":{\"content\":[{\"type\":\"text\",\"text\":\"old\"}]}}\n{\"id\":\"partial\"")
	require.NoError(t, os.WriteFile(legacy, original, 0600))
	store := NewSQLiteStorage(db, legacy)
	require.Error(t, store.Init())
	after, err := os.ReadFile(legacy)
	require.NoError(t, err)
	require.Equal(t, original, after)
	require.NoError(t, os.WriteFile(legacy, original[:len(original)-len(`{"id":"partial"`)], 0600))
	store = NewSQLiteStorage(db, legacy)
	require.NoError(t, store.Init())
	entries, err := store.GetPathToRoot(ctx, "")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "old", entries[0].User.Content[0].Text)
	require.NoError(t, store.Close())
	store = NewSQLiteStorage(db, legacy)
	require.NoError(t, store.Init())
	defer store.Close()
	entries, err = store.Entries(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestSQLiteMigrationPreservesCompactionAndBranchNavigation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	legacy := filepath.Join(dir, "session.jsonl")
	old := NewJSONLStorage(legacy)
	require.NoError(t, old.Init())
	s := New(old)
	require.NoError(t, s.AppendMessage(ctx, ai.NewTextUserMessage("original question")))
	branch, err := old.GetLeaf(ctx)
	require.NoError(t, err)
	require.NoError(t, s.AppendMessage(ctx, ai.AssistantMessage{Text: "original answer"}))
	require.NoError(t, s.AppendCompaction(ctx, "original summary"))
	require.NoError(t, s.MoveTo(ctx, branch, ""))
	require.NoError(t, s.AppendMessage(ctx, ai.AssistantMessage{Text: "branch answer"}))
	before, err := old.GetPathToRoot(ctx, "")
	require.NoError(t, err)
	require.NoError(t, old.Close())
	store := NewSQLiteStorage(filepath.Join(dir, "session.sqlite"), legacy)
	require.NoError(t, store.Init())
	defer store.Close()
	after, err := store.GetPathToRoot(ctx, "")
	require.NoError(t, err)
	require.Equal(t, before, after)
	restored := New(store)
	require.NoError(t, restored.InitFromStorage(ctx))
	messages, err := restored.BuildContext(ctx)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Equal(t, "branch answer", messages[1].(ai.AssistantMessage).Text)
	all, err := store.Entries(ctx)
	require.NoError(t, err)
	var compactFound bool
	for _, entry := range all {
		if entry.Type == EntryTypeCompaction && entry.Summary == "original summary" {
			compactFound = true
		}
	}
	require.True(t, compactFound, "off-branch compaction must remain available in durable history")
}
