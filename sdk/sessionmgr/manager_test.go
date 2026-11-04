package sessionmgr

import (
	"context"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/session"
	"github.com/stretchr/testify/require"
)

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
