package agentdata

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStewardConcurrentColdStart(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	start := make(chan struct{})
	errors := make(chan error, 16)
	for i := range 16 {
		wg.Go(func() {
			<-start
			store := New(dir)
			var err error
			if i%2 == 0 {
				_, err = store.EnsureSteward(context.Background())
			} else {
				_, err = store.ListAgents(context.Background())
			}
			errors <- err
		})
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
}

func TestStewardDatabaseCannotFollowAnOutsideSymlink(t *testing.T) {
	dir, outside := t.TempDir(), filepath.Join(t.TempDir(), "outside.sqlite")
	require.NoError(t, os.WriteFile(outside, []byte("keep"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "agents"), 0700))
	if err := os.Symlink(outside, filepath.Join(dir, "agents", "agents.sqlite")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := New(dir).EnsureSteward(context.Background())
	require.Error(t, err)
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "keep", string(data))
}
