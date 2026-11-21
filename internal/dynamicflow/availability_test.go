package dynamicflow

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAvailableRequiresCompatibleNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test node shim uses a POSIX shell")
	}
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "runtime.mjs")
	require.NoError(t, os.WriteFile(runtimePath, []byte(""), 0600))
	m, err := New(filepath.Join(dir, "runs"), runtimePath, nil)
	require.NoError(t, err)
	defer m.Close()
	t.Setenv("PATH", dir)
	require.ErrorContains(t, m.Available(), "Node.js 22.13")
	node := filepath.Join(dir, "node")
	require.NoError(t, os.WriteFile(node, []byte("#!/bin/sh\necho v22.12.0\n"), 0700))
	require.ErrorContains(t, m.Available(), "Node.js 22.13")
	require.NoError(t, os.WriteFile(node, []byte("#!/bin/sh\necho v22.13.0\n"), 0700))
	require.NoError(t, m.Available())
}
