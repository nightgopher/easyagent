package prompt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCurrentGitBranchInPlainCheckoutsAndWorktrees(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "site")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		require.NoError(t, cmd.Run(), "git %s", strings.Join(args, " "))
	}
	assert.Equal(t, "main", getCurrentGitBranch(repo))

	// A linked worktree keeps no .git directory; its .git file names the
	// metadata directory, and the branch is the worktree's own.
	cmd := exec.Command("git", "worktree", "add", "-q", "-b", "bot/news-1", filepath.Join(root, "task"))
	cmd.Dir = repo
	require.NoError(t, cmd.Run())
	assert.Equal(t, "bot/news-1", getCurrentGitBranch(filepath.Join(root, "task")))
	assert.Equal(t, "main", getCurrentGitBranch(repo))

	assert.Empty(t, getCurrentGitBranch(t.TempDir()))
	assert.Empty(t, getCurrentGitBranch(""))
}
