package sessionmgr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/session"
	"github.com/stretchr/testify/require"
)

func TestListCacheTracksHistoryAndMetadataChanges(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(t.TempDir())
	id, s := searchFixture(t, mgr, ai.NewTextUserMessage("first"))
	list, err := mgr.List(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, list[0].MessageCount)
	require.Equal(t, "first", list[0].Title)
	require.Len(t, mgr.listCache, 1)

	require.NoError(t, s.AppendMessage(ctx, ai.AssistantMessage{Text: "answer"}))
	list, err = mgr.List(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, list[0].MessageCount)
	title := "renamed"
	require.NoError(t, mgr.UpdatePreferences(id, PreferencePatch{Title: &title}))
	list, err = mgr.List(ctx)
	require.NoError(t, err)
	require.Equal(t, title, list[0].Title)

	// Replacing a transcript with identical size and timestamp must still
	// invalidate the cached file identity.
	path := mgr.SessionPath(id)
	old, err := os.Stat(path)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	replacement := filepath.Join(filepath.Dir(path), "replacement")
	require.NoError(t, os.WriteFile(replacement, data, 0600))
	require.NoError(t, os.Chtimes(replacement, old.ModTime(), old.ModTime()))
	require.NoError(t, os.Rename(replacement, path))
	_, err = mgr.List(ctx)
	require.NoError(t, err)
	newInfo, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(newInfo, mgr.listCache[path].info))
	require.False(t, os.SameFile(old, mgr.listCache[path].info))

	require.NoError(t, os.RemoveAll(filepath.Dir(path)))
	list, err = mgr.List(ctx)
	require.NoError(t, err)
	require.Empty(t, list)
	require.Empty(t, mgr.listCache)
}

func TestLegacyMigrationKeepsHistorySearchAndBackup(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(t.TempDir())
	id := "legacy"
	legacy := mgr.legacySessionPath(id)
	old := session.NewJSONLStorage(legacy)
	require.NoError(t, old.Init())
	s := session.New(old)
	require.NoError(t, s.AppendMessage(ctx, ai.NewTextUserMessage("historic task")))
	require.NoError(t, s.AppendMessage(ctx, ai.AssistantMessage{Text: "historic result"}))
	require.NoError(t, old.Close())
	backup, err := os.ReadFile(legacy)
	require.NoError(t, err)
	before, err := mgr.List(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, before[0].MessageCount)
	hits, err := mgr.Search(ctx, SearchOptions{Query: "historic result"})
	require.NoError(t, err)
	require.Len(t, hits.Hits, 1)
	migrated, path, err := mgr.Open(ctx, id)
	require.NoError(t, err)
	require.Equal(t, mgr.SessionPath(id), path)
	history, err := migrated.BuildContext(ctx)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.NoError(t, migrated.AppendMessage(ctx, ai.AssistantMessage{Text: "new sqlite result"}))
	require.NoError(t, migrated.Storage().Close())
	unchanged, err := os.ReadFile(legacy)
	require.NoError(t, err)
	require.Equal(t, backup, unchanged)
	list, err := mgr.List(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, list[0].MessageCount)
	hits, err = mgr.Search(ctx, SearchOptions{Query: "new sqlite result"})
	require.NoError(t, err)
	require.Len(t, hits.Hits, 1)
	mgr = NewManager(mgr.dataDir)
	migrated, _, err = mgr.Open(ctx, id)
	require.NoError(t, err)
	defer migrated.Storage().Close()
	history, err = migrated.BuildContext(ctx)
	require.NoError(t, err)
	require.Len(t, history, 3)
}

func TestSQLiteSessionAndSearchUseLiteralDataPath(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(filepath.Join(t.TempDir(), "folder ?#% 中文"))
	id, _, err := mgr.Create(ctx)
	require.NoError(t, err)
	s, _, err := mgr.Open(ctx, id)
	require.NoError(t, err)
	require.NoError(t, s.AppendMessage(ctx, ai.NewTextUserMessage("unique special path phrase")))
	require.NoError(t, s.Storage().Close())
	list, err := mgr.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, 1, list[0].MessageCount)
	search, err := mgr.Search(ctx, SearchOptions{Query: "unique special path phrase"})
	require.NoError(t, err)
	require.NotEmpty(t, search.Hits)
	require.Equal(t, id, search.Hits[0].SessionID)
	s, _, err = mgr.Open(ctx, id)
	require.NoError(t, err)
	defer s.Storage().Close()
	messages, err := s.BuildContext(ctx)
	require.NoError(t, err)
	require.Len(t, messages, 1)
}

func TestForkInheritsMicroCompactionWithoutChangingSourceOrOriginalOutput(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(t.TempDir())
	id, _, err := mgr.Create(ctx)
	require.NoError(t, err)
	source, _, err := mgr.Open(ctx, id)
	require.NoError(t, err)
	defer source.Storage().Close()
	history := []ai.Message{ai.NewTextUserMessage("read file"), ai.AssistantMessage{ToolCalls: []ai.ToolCall{{ID: "read", Name: "read"}}}, ai.ToolResultMessage{ToolCallID: "read", Content: "original output"}, ai.AssistantMessage{Text: "done"}}
	for _, msg := range history {
		require.NoError(t, source.AppendMessage(ctx, msg))
	}
	ids, err := source.BuildContextEntryIDs(ctx)
	require.NoError(t, err)
	cleaned := append([]ai.Message(nil), history...)
	cleaned[2] = ai.ToolResultMessage{ToolCallID: "read", Content: "cleared output"}
	require.NoError(t, source.AppendMicroCompaction(ctx, cleaned))
	fullID, _, err := mgr.ForkAt(ctx, id, nil)
	require.NoError(t, err)
	full, _, err := mgr.Open(ctx, fullID)
	require.NoError(t, err)
	defer full.Storage().Close()
	actual, err := full.BuildContext(ctx)
	require.NoError(t, err)
	require.Equal(t, cleaned, actual)
	display, err := full.BuildDisplayContext(ctx)
	require.NoError(t, err)
	require.Equal(t, history, display)
	fullIDs, err := full.BuildContextEntryIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, ids, fullIDs)
	// An earlier cut must not import a later branch's cleanup marker.
	cutID, _, err := mgr.ForkAt(ctx, id, &ids[3])
	require.NoError(t, err)
	cut, _, err := mgr.Open(ctx, cutID)
	require.NoError(t, err)
	defer cut.Storage().Close()
	actual, err = cut.BuildContext(ctx)
	require.NoError(t, err)
	require.Equal(t, history, actual)
	require.NoError(t, full.AppendCompactionKeeping(ctx, "fork summary", nil, &session.CompactionInfo{Trigger: "manual"}))
	actual, err = source.BuildContext(ctx)
	require.NoError(t, err)
	require.Equal(t, cleaned, actual, "fork writes must not affect source context")
}

func TestForkRetainedSnapshotKeepsClearedToolsAndIndependentPreferences(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(t.TempDir())
	sourceID, _, err := mgr.Create(ctx)
	require.NoError(t, err)
	require.NoError(t, mgr.SaveMeta(sourceID, "/project", "coding"))
	title, flag := "source", true
	require.NoError(t, mgr.UpdatePreferences(sourceID, PreferencePatch{Title: &title, Pinned: &flag, Archived: &flag}))
	source, _, err := mgr.Open(ctx, sourceID)
	require.NoError(t, err)
	defer source.Storage().Close()
	original := []ai.Message{ai.NewTextUserMessage("question"), ai.AssistantMessage{ToolCalls: []ai.ToolCall{{ID: "call", Name: "read"}}}, ai.ToolResultMessage{ToolCallID: "call", Content: "large original output"}}
	for _, msg := range original {
		require.NoError(t, source.AppendMessage(ctx, msg))
	}
	ids, err := source.BuildContextEntryIDs(ctx)
	require.NoError(t, err)
	retained := append([]ai.Message(nil), original...)
	retained[2] = ai.ToolResultMessage{ToolCallID: "call", Content: "[cleared]"}
	require.NoError(t, source.AppendCompactionKeeping(ctx, "summary", retained, nil))
	newID, _, err := mgr.ForkAt(ctx, sourceID, &ids[2])
	require.NoError(t, err)
	fork, _, err := mgr.Open(ctx, newID)
	require.NoError(t, err)
	defer fork.Storage().Close()
	messages, err := fork.BuildContext(ctx)
	require.NoError(t, err)
	require.Len(t, messages, 4)
	require.Equal(t, "[cleared]", messages[3].(ai.ToolResultMessage).Content)
	infos, err := mgr.List(ctx)
	require.NoError(t, err)
	for _, info := range infos {
		if info.ID == newID {
			require.False(t, info.Archived)
			require.False(t, info.Pinned)
			require.Equal(t, "/project", info.Workspace)
			require.Equal(t, sourceID, info.ForkedFrom)
		}
	}
	invalid := "absent"
	_, _, err = mgr.ForkAt(ctx, sourceID, &invalid)
	require.Error(t, err)
	infos, err = mgr.List(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 2)
}

func TestOpenRejectsTraversalAndSymlinkSessionDirectories(t *testing.T) {
	root := t.TempDir()
	mgr := NewManager(root)
	require.NoError(t, os.MkdirAll(mgr.SessionsDir(), 0700))
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.Mkdir(outside, 0700))
	for _, id := range []string{"", ".", "..", "../outside", `..\outside`} {
		_, _, err := mgr.Open(context.Background(), id)
		require.ErrorContains(t, err, "invalid session id")
		require.False(t, mgr.Exists(id))
	}
	_, err := os.Stat(filepath.Join(outside, "session.sqlite"))
	require.True(t, os.IsNotExist(err), "invalid IDs must not create a database outside sessions")
	if err := os.Symlink(outside, filepath.Join(mgr.SessionsDir(), "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, _, err = mgr.Open(context.Background(), "linked")
	require.ErrorContains(t, err, "not a regular directory")
	require.False(t, mgr.Exists("linked"))
	_, err = os.Stat(filepath.Join(outside, "session.sqlite"))
	require.True(t, os.IsNotExist(err), "a symlink must not create a database outside sessions")
}

func TestListActivityIgnoresOpeningAndNonMessageChanges(t *testing.T) {
	ctx := context.Background()
	for _, legacy := range []bool{false, true} {
		name := "sqlite"
		if legacy {
			name = "jsonl"
		}
		t.Run(name, func(t *testing.T) {
			mgr := NewManager(t.TempDir())
			ids := []string{"older", "newer"}
			for i, id := range ids {
				var storage session.SessionStorage
				if legacy {
					storage = session.NewJSONLStorage(mgr.legacySessionPath(id))
				} else {
					storage = session.NewSQLiteStorage(mgr.SessionPath(id), mgr.legacySessionPath(id))
				}
				require.NoError(t, storage.Init())
				msg := ai.NewTextUserMessage(id)
				require.NoError(t, storage.Append(ctx, session.Entry{Type: session.EntryTypeMessage, Timestamp: int64(1700000000000 + i*10000), User: &msg}))
				if id == "older" {
					require.NoError(t, storage.Append(ctx, session.Entry{Type: session.EntryTypeModelChange, Timestamp: 1900000000000}))
				}
				require.NoError(t, storage.Close())
			}
			list, err := mgr.List(ctx)
			require.NoError(t, err)
			require.Equal(t, "newer", list[0].ID)
			require.Equal(t, int64(1700000000), list[1].LastActive)
			// Opening a session and replacing metadata both touch files in its directory.
			opened, _, err := mgr.Open(ctx, "older")
			require.NoError(t, err)
			require.NoError(t, opened.Storage().Append(ctx, session.Entry{Type: session.EntryTypeModelChange, Timestamp: 1900000000000}))
			require.NoError(t, opened.Storage().Close())
			title := "renamed"
			require.NoError(t, mgr.UpdatePreferences("older", PreferencePatch{Title: &title}))
			list, err = mgr.List(ctx)
			require.NoError(t, err)
			require.Equal(t, "newer", list[0].ID)
			require.Equal(t, int64(1700000000), list[1].LastActive)
			opened, _, err = mgr.Open(ctx, "older")
			require.NoError(t, err)
			msg := ai.NewTextUserMessage("new message")
			require.NoError(t, opened.Storage().Append(ctx, session.Entry{Type: session.EntryTypeMessage, Timestamp: 1800000000000, User: &msg}))
			require.NoError(t, opened.Storage().Close())
			list, err = mgr.List(ctx)
			require.NoError(t, err)
			require.Equal(t, "older", list[0].ID)
			require.Equal(t, int64(1800000000), list[0].LastActive)
		})
	}
}

func TestEmptySessionActivityUsesStableCreationTime(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(t.TempDir())
	id, _, err := mgr.Create(ctx)
	require.NoError(t, err)
	before, err := mgr.List(ctx)
	require.NoError(t, err)
	require.Len(t, before, 1)
	future := time.Now().Add(24 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(mgr.SessionsDir(), id), future, future))
	after, err := mgr.List(ctx)
	require.NoError(t, err)
	require.Equal(t, before[0].CreatedAt, after[0].CreatedAt)
	require.Equal(t, before[0].LastActive, after[0].LastActive)
}
