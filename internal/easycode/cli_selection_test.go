package easycode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCLISelectionPersistsAndCanBeDisabledWithoutLogout(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	m, err := NewManager(home, "")
	require.NoError(t, err)
	model, err := m.CLIModel(ctx)
	require.NoError(t, err)
	require.Empty(t, model)
	_, err = os.Stat(filepath.Join(home, "easycode"))
	require.True(t, os.IsNotExist(err), "reading an unset selection should not create a directory")

	require.NoError(t, m.save(ctx, credentials{AccessToken: "fixture", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}))
	require.NoError(t, m.SetCLIModel(ctx, "model-a"))
	info, err := os.Stat(filepath.Join(home, "easycode", cliSelectionFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	other, err := NewManager(home, "")
	require.NoError(t, err)
	model, err = other.CLIModel(ctx)
	require.NoError(t, err)
	require.Equal(t, "model-a", model)
	for _, invalid := range []string{"", " model", "model\n", strings.Repeat("x", 201)} {
		require.Error(t, m.SetCLIModel(ctx, invalid))
	}
	model, err = m.CLIModel(ctx)
	require.NoError(t, err)
	require.Equal(t, "model-a", model)
	require.NoError(t, m.ClearCLIModel(ctx))
	_, loggedIn, err := m.Status(ctx)
	require.NoError(t, err)
	require.True(t, loggedIn)
	require.NoError(t, m.SetCLIModel(ctx, "model-b"))
	require.NoError(t, m.Logout(ctx))
	model, err = m.CLIModel(ctx)
	require.NoError(t, err)
	require.Empty(t, model)
	_, loggedIn, err = m.Status(ctx)
	require.NoError(t, err)
	require.False(t, loggedIn)
}

func TestCLISelectionRejectsInsecureFile(t *testing.T) {
	ctx := context.Background()
	m, err := NewManager(t.TempDir(), "")
	require.NoError(t, err)
	require.NoError(t, m.SetCLIModel(ctx, "model-a"))
	path := filepath.Join(m.dir, cliSelectionFile)
	require.NoError(t, os.Chmod(path, 0o644))
	_, err = m.CLIModel(ctx)
	require.Error(t, err)
	require.NoError(t, os.Remove(path))
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(target, []byte(`{"model":"model-b"}`), 0o600))
	require.NoError(t, os.Symlink(target, path))
	_, err = m.CLIModel(ctx)
	require.Error(t, err)
}
