package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectContextLoadsGlobalMemoryAlongsideClaude(t *testing.T) {
	root := t.TempDir()
	agentDir, cwd := filepath.Join(root, "agent"), filepath.Join(root, "project")
	require.NoError(t, os.MkdirAll(agentDir, 0o755))
	require.NoError(t, os.MkdirAll(cwd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "CLAUDE.md"), []byte("global instructions"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "AGENTS.md"), []byte("remembered fact"), 0o644))
	files := LoadProjectContextFiles(cwd, agentDir)
	require.Len(t, files, 2)
	require.Equal(t, "global instructions", files[0].Content)
	require.Equal(t, "remembered fact", files[1].Content)
}

func TestLoadContextFiles_Priority(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("Claude"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Agents"), 0o644))

	files := LoadContextFiles(dir)
	require.Len(t, files, 1)
	require.Equal(t, "Claude", files[0].Content)
}

func TestLoadProjectContextFiles_AncestorTraversal(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(child, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("Root instructions"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "AGENTS.md"), []byte("Mid instructions"), 0o644))

	files := LoadProjectContextFiles(child, "")
	require.Len(t, files, 2)
	require.Contains(t, files[0].Content, "Root")
	require.Contains(t, files[1].Content, "Mid")
}

func TestLoadProjectContextFiles_WithAgentDir(t *testing.T) {
	agentDir := t.TempDir()
	cwd := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "CLAUDE.md"), []byte("Global instructions"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cwd, "CLAUDE.md"), []byte("Local instructions"), 0o644))

	files := LoadProjectContextFiles(cwd, agentDir)
	require.Len(t, files, 2)
	require.Contains(t, files[0].Content, "Global")
	require.Contains(t, files[1].Content, "Local")
}
