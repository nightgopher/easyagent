//go:build unix

package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSSHFileCreateAndRemoveUseRemoteCommandSafely(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	// The fake SSH executes the exact generated remote command locally in an
	// isolated directory while preserving stdin and argument boundaries.
	ssh := "#!/bin/sh\nfor arg in \"$@\"; do remote=$arg; done\nexec /bin/sh -c \"$remote\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ssh"), []byte(ssh), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ops := NewSSHOperations(SSHConfig{Host: "fixture"}).Files
	creator := ops.(FileCreator)
	remover := ops.(FileRemover)
	path := filepath.Join(dir, "file's name.txt")
	content := []byte{0, 1, 2, '\n'}
	require.NoError(t, creator.CreateFile(context.Background(), path, content, 0o640))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, got)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	require.Error(t, creator.CreateFile(context.Background(), path, []byte("overwrite"), 0o600))
	got, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, got)
	matches, err := filepath.Glob(filepath.Join(dir, ".easyagent-patch.*"))
	require.NoError(t, err)
	require.Empty(t, matches)
	require.NoError(t, remover.RemoveFile(context.Background(), path))
	require.NoFileExists(t, path)
}
