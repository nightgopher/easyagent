package app

import (
	"context"
	"testing"

	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/stretchr/testify/require"
)

// A session created before the removal must stay readable and must not fall
// back to the default application's file and shell tools.
func TestRetiredMusicSessionStaysReadableWithoutDefaultTools(t *testing.T) {
	ctx := context.Background()
	a := taskTestApp(t, nil)
	created, err := a.SessionStore().Create(ctx, runtime.AgentSessionOptions{Config: a.Config()}, a.SessionDepsWithApp(""))
	require.NoError(t, err)
	id, workspace := created.SessionID(), created.Workspace()
	require.NoError(t, a.SessionManager().SaveMeta(id, workspace, retiredMusicApplication))
	require.NoError(t, a.SessionStore().Delete(id))

	restored, err := a.LoadSession(ctx, id)
	require.NoError(t, err)
	require.Equal(t, workspace, restored.Workspace())
	names := restored.ToolNames()
	require.Subset(t, names, retiredMusicToolNames)
	for _, name := range []string{"bash", "read", "write", "edit", "task"} {
		require.NotContains(t, names, name)
	}
	_, application := a.SessionManager().Metadata(id)
	require.Equal(t, retiredMusicApplication, application)

	result, err := retiredMusicTool{name: "music_play"}.Execute(ctx, nil, nil)
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.Content, "已下线")
}

func TestSessionSetupRejectsUnknownApplications(t *testing.T) {
	a := taskTestApp(t, nil)
	for _, application := range []string{retiredMusicApplication, "unknown"} {
		_, _, err := a.SessionSetup(context.Background(), application, "", a.Config())
		require.Error(t, err, application)
	}
	for _, application := range []string{"", "coding"} {
		_, _, err := a.SessionSetup(context.Background(), application, "", a.Config())
		require.NoError(t, err, application)
	}
	require.NotContains(t, a.ApplicationNames(), retiredMusicApplication)
}
