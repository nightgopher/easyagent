package app

import (
	"context"
	"testing"

	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/stretchr/testify/require"
)

func TestRetiredKBSessionRemainsReadableWithoutCodingTools(t *testing.T) {
	ctx := context.Background()
	a := taskTestApp(t, nil)
	created, err := a.SessionStore().Create(ctx, runtime.AgentSessionOptions{Config: a.Config()}, a.SessionDepsWithApp(""))
	require.NoError(t, err)
	id := created.SessionID()
	require.NoError(t, a.SessionManager().SaveMeta(id, created.Workspace(), retiredKBApplication))
	require.NoError(t, a.SessionStore().Delete(id))
	restored, err := a.LoadSession(ctx, id)
	require.NoError(t, err)
	require.Subset(t, restored.ToolNames(), retiredKBToolNames)
	for _, name := range []string{"bash", "read", "write", "edit", "task"} {
		require.NotContains(t, restored.ToolNames(), name)
	}
	result, err := (retiredKBTool{name: "kb_save"}).Execute(ctx, nil, nil)
	require.NoError(t, err)
	require.True(t, result.IsError)
	_, _, err = a.SessionSetup(ctx, retiredKBApplication, "", a.Config())
	require.Error(t, err)
	require.NotContains(t, a.ApplicationNames(), retiredKBApplication)
}
