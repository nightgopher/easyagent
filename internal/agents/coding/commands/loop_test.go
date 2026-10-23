package commands

import (
	"context"
	"testing"

	"github.com/hwj123hwj/easyagent/internal/scheduler"
	"github.com/hwj123hwj/easyagent/sdk/slashcmd"
	"github.com/stretchr/testify/require"
)

type loopCommandSession struct{ slashcmd.SessionContext }

func (loopCommandSession) SessionID() string { return "test-session" }

func TestLoopCommandRequiresSupportedEntrypoint(t *testing.T) {
	mgr := scheduler.NewLoopManager()
	defer mgr.StopAll()
	registry := slashcmd.NewRegistry()
	RegisterLoopCommands(registry, mgr)
	ctx := slashcmd.Context{Ctx: context.Background(), Session: loopCommandSession{}}

	result, err := registry.Execute(ctx, "/loop 1m run tests")
	require.ErrorIs(t, err, scheduler.ErrNoTrigger)
	require.Contains(t, err.Error(), "without --legacy")
	require.Empty(t, result.Output)
	require.Nil(t, mgr.Get("test-session"))

	mgr.SetTriggerResolver(func(string) scheduler.TriggerFunc {
		return func(context.Context, string) error { return nil }
	})
	result, err = registry.Execute(ctx, "/loop 1m run tests --expires 1h")
	require.NoError(t, err)
	require.Contains(t, result.Output, "Loop scheduled")
	require.Equal(t, "run tests", mgr.Get("test-session").Prompt)
	result, err = registry.Execute(ctx, "/loop")
	require.NoError(t, err)
	require.Contains(t, result.Output, "Last Run: never")
	result, err = registry.Execute(ctx, "/loop clear")
	require.NoError(t, err)
	require.Contains(t, result.Output, "Loop stopped")
	require.Nil(t, mgr.Get("test-session"))
}
