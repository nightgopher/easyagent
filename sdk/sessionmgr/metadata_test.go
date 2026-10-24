package sessionmgr_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
	"github.com/stretchr/testify/require"
)

func sessionInfo(t *testing.T, manager *sessionmgr.Manager, id string) sessionmgr.SessionInfo {
	t.Helper()
	infos, err := manager.List(context.Background())
	require.NoError(t, err)
	for _, info := range infos {
		if info.ID == id {
			return info
		}
	}
	t.Fatalf("session %s missing from listing", id)
	return sessionmgr.SessionInfo{}
}

func TestMetadataDefaultsSupportForkAndUpdates(t *testing.T) {
	for _, initial := range []string{"missing", "null", "{}"} {
		t.Run(initial, func(t *testing.T) {
			ctx := context.Background()
			manager := sessionmgr.NewManager(t.TempDir())
			id, _, err := manager.Create(ctx)
			require.NoError(t, err)
			source, _, err := manager.Open(ctx, id)
			require.NoError(t, err)
			require.NoError(t, source.AppendMessage(ctx, ai.NewTextUserMessage("源会话\n详细问题")))
			require.NoError(t, source.Storage().Close())
			metaPath := filepath.Join(manager.SessionsDir(), id, "meta.json")
			if initial != "missing" {
				require.NoError(t, os.WriteFile(metaPath, []byte(initial), 0o600))
			}

			workspace, application := manager.Metadata(id)
			require.Empty(t, workspace)
			require.Empty(t, application)
			choice, err := manager.LoadModel(id)
			require.NoError(t, err)
			require.Zero(t, choice)
			require.Equal(t, "源会话", sessionInfo(t, manager, id).Title)

			forkID, _, err := manager.Fork(ctx, id, "")
			require.NoError(t, err)
			fork, _, err := manager.Open(ctx, forkID)
			require.NoError(t, err)
			history, err := fork.BuildContext(ctx)
			require.NoError(t, err)
			require.Equal(t, []ai.Message{ai.NewTextUserMessage("源会话\n详细问题")}, history)
			require.NoError(t, fork.Storage().Close())
			info := sessionInfo(t, manager, forkID)
			require.Equal(t, "源会话 (分叉)", info.Title)
			require.Equal(t, id, info.ForkedFrom)
			require.False(t, info.Pinned)
			require.False(t, info.Archived)
			if initial == "missing" {
				require.NoFileExists(t, metaPath)
			} else {
				data, err := os.ReadFile(metaPath)
				require.NoError(t, err)
				require.Equal(t, initial, string(data))
			}

			require.NoError(t, manager.SaveMeta(id, "/project", "coding"))
			wantChoice := sessionmgr.ModelPreference{Model: "test-model", Provider: "openai"}
			require.NoError(t, manager.SaveModel(id, wantChoice))
			title, pinned := "新标题", true
			require.NoError(t, manager.UpdatePreferences(id, sessionmgr.PreferencePatch{Title: &title, Pinned: &pinned}))
			info = sessionInfo(t, manager, id)
			require.Equal(t, "/project", info.Workspace)
			require.Equal(t, "coding", info.Application)
			require.Equal(t, title, info.Title)
			require.True(t, info.Pinned)
			choice, err = manager.LoadModel(id)
			require.NoError(t, err)
			require.Equal(t, wantChoice, choice)
		})
	}
}

func TestMetadataUpdatesAndForkPreserveUnknownFields(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	manager := sessionmgr.NewManager(root)
	id, _, err := manager.Create(ctx)
	require.NoError(t, err)
	metaPath := filepath.Join(manager.SessionsDir(), id, "meta.json")
	const extension = `{"id":9007199254740993,"nested":["保留",true,null]}`
	require.NoError(t, os.WriteFile(metaPath, []byte(`{"future":`+extension+`,"future_null":null}`), 0o600))
	require.NoError(t, manager.SaveMeta(id, "/project", "music"))
	choice := sessionmgr.ModelPreference{Model: "test-model", Provider: "openai"}
	require.NoError(t, manager.SaveModel(id, choice))
	title, flag := "我的会话", true
	require.NoError(t, manager.UpdatePreferences(id, sessionmgr.PreferencePatch{Title: &title, Pinned: &flag, Archived: &flag}))
	original, err := os.ReadFile(metaPath)
	require.NoError(t, err)

	for _, application := range []*string{nil, new(string)} {
		forkID, _, err := manager.ForkSnapshot(ctx, id, nil, nil, application)
		require.NoError(t, err)
		info := sessionInfo(t, manager, forkID)
		require.Equal(t, "/project", info.Workspace)
		wantApplication := "music"
		if application != nil {
			wantApplication = *application
		}
		require.Equal(t, wantApplication, info.Application)
		require.Equal(t, title+" (分叉)", info.Title)
		require.Equal(t, id, info.ForkedFrom)
		require.False(t, info.Pinned)
		require.False(t, info.Archived)
		forkChoice, err := manager.LoadModel(forkID)
		require.NoError(t, err)
		require.Equal(t, choice, forkChoice)

		forkTitle := "分叉的新标题"
		require.NoError(t, manager.UpdatePreferences(forkID, sessionmgr.PreferencePatch{Title: &forkTitle}))
		for _, sessionID := range []string{id, forkID} {
			data, err := os.ReadFile(filepath.Join(manager.SessionsDir(), sessionID, "meta.json"))
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &fields))
			require.Equal(t, extension, string(fields["future"]))
			require.Equal(t, "null", string(fields["future_null"]))
		}
	}

	data, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	require.Equal(t, original, data, "forks must not rewrite their source metadata")
	reloaded := sessionmgr.NewManager(root)
	info := sessionInfo(t, reloaded, id)
	require.Equal(t, title, info.Title)
	require.True(t, info.Pinned)
	require.True(t, info.Archived)
	workspace, application := reloaded.Metadata(id)
	require.Equal(t, "/project", workspace)
	require.Equal(t, "music", application)
}

func TestMetadataAcceptsLegacyFieldCasing(t *testing.T) {
	manager := sessionmgr.NewManager(t.TempDir())
	id, _, err := manager.Create(context.Background())
	require.NoError(t, err)
	metaPath := filepath.Join(manager.SessionsDir(), id, "meta.json")
	require.NoError(t, os.WriteFile(metaPath, []byte(`{"Workspace":"/legacy","Application":"kb","TITLE":"旧标题","PINNED":true,"Archived":true,"FORKED_FROM":"parent","MODEL_SELECTION":{"model":"chosen","provider":"openai"}}`), 0o600))

	workspace, application := manager.Metadata(id)
	require.Equal(t, "/legacy", workspace)
	require.Equal(t, "kb", application)
	choice, err := manager.LoadModel(id)
	require.NoError(t, err)
	require.Equal(t, sessionmgr.ModelPreference{Model: "chosen", Provider: "openai"}, choice)
	info := sessionInfo(t, manager, id)
	require.Equal(t, workspace, info.Workspace)
	require.Equal(t, application, info.Application)
	require.Equal(t, "旧标题", info.Title)
	require.True(t, info.Pinned)
	require.True(t, info.Archived)
	require.Equal(t, "parent", info.ForkedFrom)

	// Canonical fields written by current callers take precedence over aliases.
	require.NoError(t, manager.SaveMeta(id, "/current", "coding"))
	choice = sessionmgr.ModelPreference{Model: "current", Provider: "anthropic"}
	require.NoError(t, manager.SaveModel(id, choice))
	title, flag := "新标题", false
	require.NoError(t, manager.UpdatePreferences(id, sessionmgr.PreferencePatch{Title: &title, Pinned: &flag, Archived: &flag}))
	info = sessionInfo(t, manager, id)
	require.Equal(t, "/current", info.Workspace)
	require.Equal(t, "coding", info.Application)
	require.Equal(t, title, info.Title)
	require.False(t, info.Pinned)
	require.False(t, info.Archived)
	actualChoice, err := manager.LoadModel(id)
	require.NoError(t, err)
	require.Equal(t, choice, actualChoice)
}

func TestInvalidMetadataRejectedWithoutDataLoss(t *testing.T) {
	for _, initial := range []string{`{"workspace":`, `[]`, `{"workspace":"/project"} {}`} {
		t.Run(initial, func(t *testing.T) {
			ctx := context.Background()
			manager := sessionmgr.NewManager(t.TempDir())
			id, _, err := manager.Create(ctx)
			require.NoError(t, err)
			metaPath := filepath.Join(manager.SessionsDir(), id, "meta.json")
			require.NoError(t, os.WriteFile(metaPath, []byte(initial), 0o600))

			_, err = manager.LoadModel(id)
			require.Error(t, err)
			require.Error(t, manager.SaveMeta(id, "/project", "coding"))
			require.Error(t, manager.SaveModel(id, sessionmgr.ModelPreference{Model: "test-model"}))
			title := "新标题"
			require.Error(t, manager.UpdatePreferences(id, sessionmgr.PreferencePatch{Title: &title}))
			_, _, err = manager.Fork(ctx, id, "")
			require.Error(t, err)

			data, err := os.ReadFile(metaPath)
			require.NoError(t, err)
			require.Equal(t, initial, string(data))
			workspace, application := manager.Metadata(id)
			require.Empty(t, workspace)
			require.Empty(t, application)
			infos, err := manager.List(ctx)
			require.NoError(t, err)
			require.Len(t, infos, 1, "failed forks must not leave another session")
			require.Equal(t, id, infos[0].ID)
			dirs, err := os.ReadDir(manager.SessionsDir())
			require.NoError(t, err)
			require.Len(t, dirs, 1)
		})
	}
}

func TestConcurrentMetadataUpdatesPreserveIndependentFields(t *testing.T) {
	manager := sessionmgr.NewManager(t.TempDir())
	id, _, err := manager.Create(context.Background())
	require.NoError(t, err)
	title, flag := "并发更新", true
	choice := sessionmgr.ModelPreference{Model: "test-model", Provider: "openai"}
	writes := []func() error{
		func() error { return manager.SaveMeta(id, "/project", "coding") },
		func() error { return manager.SaveModel(id, choice) },
		func() error {
			return manager.UpdatePreferences(id, sessionmgr.PreferencePatch{Title: &title, Pinned: &flag, Archived: &flag})
		},
	}
	errors := make(chan error, len(writes))
	var wg sync.WaitGroup
	for _, write := range writes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if err := write(); err != nil {
					errors <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	info := sessionInfo(t, manager, id)
	require.Equal(t, "/project", info.Workspace)
	require.Equal(t, "coding", info.Application)
	require.Equal(t, title, info.Title)
	require.True(t, info.Pinned)
	require.True(t, info.Archived)
	actualChoice, err := manager.LoadModel(id)
	require.NoError(t, err)
	require.Equal(t, choice, actualChoice)
}
