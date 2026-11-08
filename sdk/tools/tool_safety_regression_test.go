package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/operations"
	"github.com/stretchr/testify/require"
)

func executePatchForTest(t *testing.T, tool *PatchTool, patch string) error {
	t.Helper()
	params, err := json.Marshal(PatchParams{PatchText: patch})
	require.NoError(t, err)
	params, err = tool.Validate(params)
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), params, nil)
	if err == nil && result.IsError {
		return os.ErrInvalid
	}
	return err
}

func TestPatchRejectsMismatchedHunkWithoutChangingAnyFile(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first.txt"), filepath.Join(dir, "second.txt")
	require.NoError(t, os.WriteFile(first, []byte("first\n"), 0o644))
	require.NoError(t, os.WriteFile(second, []byte("alpha\nactual\nomega\n"), 0o644))
	patch := "--- a/first.txt\n+++ b/first.txt\n@@ -1 +1 @@\n-first\n+changed\n" +
		"--- a/second.txt\n+++ b/second.txt\n@@ -1,3 +1,3 @@\n alpha\n-expected\n+changed\n omega\n"
	require.Error(t, executePatchForTest(t, NewPatchTool(WithPatchWorkspace(dir)), patch))
	gotFirst, err := os.ReadFile(first)
	require.NoError(t, err)
	require.Equal(t, "first\n", string(gotFirst))
	gotSecond, err := os.ReadFile(second)
	require.NoError(t, err)
	require.Equal(t, "alpha\nactual\nomega\n", string(gotSecond))
}

func TestPatchDeletesOnlyMatchingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o644))
	tool := NewPatchTool(WithPatchWorkspace(dir))
	require.Error(t, executePatchForTest(t, tool, "--- a/note.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-wrong\n"))
	require.FileExists(t, path)
	require.NoError(t, executePatchForTest(t, tool, "--- a/note.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n"))
	require.NoFileExists(t, path)
}

type remoteDeleteTestOps struct {
	operations.LocalFileOperations
	removed bool
}

func (o *remoteDeleteTestOps) Stat(context.Context, string) (operations.FileInfo, error) {
	return operations.FileInfo{}, nil
}

func (o *remoteDeleteTestOps) ReadFile(context.Context, string) ([]byte, error) {
	return []byte("remote text\n"), nil
}

func (o *remoteDeleteTestOps) RemoveFile(context.Context, string) error {
	o.removed = true
	return nil
}

func TestDeleteFileUsesConfiguredBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same-path.txt")
	require.NoError(t, os.WriteFile(path, []byte("local file\n"), 0o644))
	ops := &remoteDeleteTestOps{}
	tool := NewDeleteFileTool(WithDeleteFileWorkspace(dir), WithDeleteFileOperations(ops))
	params, err := json.Marshal(DeleteFileParams{FilePath: path})
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), params, nil)
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.True(t, ops.removed)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "local file\n", string(data))
}

func TestDeleteFileRejectsBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary")
	require.NoError(t, os.WriteFile(path, []byte{0, 1, 2}, 0o644))
	tool := NewDeleteFileTool(WithDeleteFileWorkspace(dir))
	params, err := json.Marshal(DeleteFileParams{FilePath: path})
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), params, nil)
	require.True(t, err != nil || result.IsError)
	require.FileExists(t, path)
}

func TestPatchMultipleHunksAndMissingFinalNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script")
	require.NoError(t, os.WriteFile(path, []byte("first\nunchanged\nlast"), 0o755))
	patch := "--- a/script\n+++ b/script\n@@ -1 +1 @@\n-first\n+FIRST\n" +
		"@@ -3 +3 @@\n-last\n\\ No newline at end of file\n+LAST\n\\ No newline at end of file\n"
	require.NoError(t, executePatchForTest(t, NewPatchTool(WithPatchWorkspace(dir)), patch))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "FIRST\nunchanged\nLAST", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

func TestPatchAddsFileWithoutOverwritingExistingFile(t *testing.T) {
	dir := t.TempDir()
	tool := NewPatchTool(WithPatchWorkspace(dir))
	patch := "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+created\n"
	require.NoError(t, executePatchForTest(t, tool, patch))
	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "created\n", string(data))
	require.Error(t, executePatchForTest(t, tool, patch))
	data, err = os.ReadFile(filepath.Join(dir, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "created\n", string(data))
}

type racingPatchCreateOps struct{ operations.LocalFileOperations }

func (racingPatchCreateOps) CreateFile(_ context.Context, path string, _ []byte, _ os.FileMode) error {
	if err := os.WriteFile(path, []byte("created by someone else\n"), 0o644); err != nil {
		return err
	}
	return os.ErrExist
}

func TestPatchDoesNotRemoveFileCreatedByAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	tool := NewPatchTool(WithPatchWorkspace(dir), WithPatchOperations(racingPatchCreateOps{}))
	patch := "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+created\n"
	require.Error(t, executePatchForTest(t, tool, patch))
	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "created by someone else\n", string(data))
}

func TestDestructiveToolsRequireAnApprovalChannel(t *testing.T) {
	require.True(t, NewPatchTool().RequiresConfirmationAvailable())
	require.True(t, NewDeleteFileTool().RequiresConfirmationAvailable())
}

func TestPatchRejectsMalformedOrStaleChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note")
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o644))
	tracker := NewReadTracker()
	tracker.Record(path, []byte("different\n"), time.Time{})
	tool := NewPatchTool(WithPatchWorkspace(dir), WithPatchReadTracker(tracker))
	for _, patch := range []string{
		"--- a/note\n+++ b/note\n@@ -1,2 +1 @@\n-old\n+new\n",
		"--- a/note\n+++ b/note\n@@ -1 +1 @@\n-old\n+new\n",
	} {
		require.Error(t, executePatchForTest(t, tool, patch))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "old\n", string(data))
	}
}

type failPatchWriteOps struct {
	operations.LocalFileOperations
	failPath string
	failed   bool
}

func (o *failPatchWriteOps) WriteFile(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	if path == o.failPath && !o.failed {
		o.failed = true
		return errors.New("injected write failure")
	}
	return o.LocalFileOperations.WriteFile(ctx, path, data, mode)
}

func TestPatchRollsBackEarlierFilesOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	require.NoError(t, os.WriteFile(first, []byte("one\n"), 0o644))
	require.NoError(t, os.WriteFile(second, []byte("two\n"), 0o644))
	ops := &failPatchWriteOps{failPath: second}
	patch := "--- a/first\n+++ b/first\n@@ -1 +1 @@\n-one\n+ONE\n" +
		"--- a/second\n+++ b/second\n@@ -1 +1 @@\n-two\n+TWO\n"
	require.Error(t, executePatchForTest(t, NewPatchTool(WithPatchWorkspace(dir), WithPatchOperations(ops)), patch))
	for path, expected := range map[string]string{first: "one\n", second: "two\n"} {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, expected, string(data))
	}
}

type concurrentPatchWriteOps struct {
	operations.LocalFileOperations
	first, second string
}

func (o *concurrentPatchWriteOps) WriteFile(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	if path == o.second {
		if err := os.WriteFile(o.first, []byte("outside change\n"), 0o644); err != nil {
			return err
		}
		return errors.New("injected write failure")
	}
	return o.LocalFileOperations.WriteFile(ctx, path, data, mode)
}

func TestPatchRollbackDoesNotOverwriteConcurrentChanges(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	require.NoError(t, os.WriteFile(first, []byte("one\n"), 0o644))
	require.NoError(t, os.WriteFile(second, []byte("two\n"), 0o644))
	ops := &concurrentPatchWriteOps{first: first, second: second}
	patch := "--- a/first\n+++ b/first\n@@ -1 +1 @@\n-one\n+ONE\n" +
		"--- a/second\n+++ b/second\n@@ -1 +1 @@\n-two\n+TWO\n"
	err := executePatchForTest(t, NewPatchTool(WithPatchWorkspace(dir), WithPatchOperations(ops)), patch)
	require.ErrorContains(t, err, "rollback failed")
	data, err := os.ReadFile(first)
	require.NoError(t, err)
	require.Equal(t, "outside change\n", string(data))
}
