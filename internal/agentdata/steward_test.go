package agentdata

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStewardPresetNeverResetsSavedPermissions(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	store := New(dir)
	first, err := store.EnsureSteward(ctx)
	require.NoError(t, err)
	require.Equal(t, StewardID, first.ID)
	require.NotContains(t, first.Tools, "write")
	require.NotContains(t, first.Tools, "task")
	first.Instructions, first.Tools, first.Disabled, first.MemoryEnabled = "我的管家说明", []string{"local_time"}, true, false
	first, err = store.SaveAgent(ctx, first)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errors := make(chan error, 4)
	for range 4 {
		wg.Go(func() { _, err := New(dir).EnsureSteward(ctx); errors <- err })
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	reopened, err := New(dir).EnsureSteward(ctx)
	require.NoError(t, err)
	require.Equal(t, first, reopened)
	all, err := store.ListAgents(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
}
