package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nightgopher/easyagent/sdk/operations"
)

func TestMatchPathGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"src/content/**", "src/content", true},
		{"src/content/**", "src/content/zh-CN/news/a.md", true},
		{"src/content/**", "src/content/zh-CN/news", true},
		{"src/content/**", "src/data/products.ts", false},
		{"src/content/**", "src/contents/a.md", false},
		{"src/content/**", "src/../astro.config.mjs", false},
		{"**/*.md", "docs/a/b/c.md", true},
		{"**/*.md", "docs/a/b/c.txt", false},
		{"src/*.ts", "src/products.ts", true},
		{"src/*.ts", "src/data/products.ts", false},
		{"src/*.ts", "src/products.tsx", false},
		{"public/**", "public", true},
		{".agents/tmp/**", ".agents/tmp/inbox/a.png", true},
		{"*", "astro.config.mjs", true},
		{"*", "src/x", false},
		{"src/[pc]/**", "src/c/x", true},
		{"src/[pc]/**", "src/d/x", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, MatchPathGlob(tc.pattern, tc.name), "%q vs %q", tc.pattern, tc.name)
	}
}

func TestWritePolicyCheck(t *testing.T) {
	policy := WritePolicy{
		Allow: []string{"src/content/**", "src/data/**", ".agents/tmp/**"},
		Deny:  []string{"src/data/secrets/**"},
	}
	require.NoError(t, policy.Check("src/content/zh-CN/news/a.md"))
	require.NoError(t, policy.Check(".agents/tmp/inbox/a.png"))
	assert.ErrorContains(t, policy.Check("astro.config.mjs"), "outside the writable paths")
	assert.ErrorContains(t, policy.Check("src/data/secrets/token.ts"), "protected pattern")
	assert.ErrorContains(t, policy.Check("src/components/products/X.astro"), "outside the writable paths")

	// A deny list alone protects paths and leaves the rest of the workspace writable.
	denyOnly := WritePolicy{Deny: []string{"astro.config.*"}}
	require.NoError(t, denyOnly.Check("src/anything.ts"))
	assert.Error(t, denyOnly.Check("astro.config.mjs"))
	assert.False(t, WritePolicy{}.Configured())
}

// newPolicyWorkspace builds a workspace with the guard profile's layout and
// a link inside a writable directory that points at a protected file.
func newPolicyWorkspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	for _, dir := range []string{"src/content/news", "src/data", "src/components/products", ".agents/tmp"} {
		require.NoError(t, os.MkdirAll(filepath.Join(ws, dir), 0o755))
	}
	for _, file := range []string{"astro.config.mjs", "src/content/news/a.md", "src/components/products/X.astro"} {
		require.NoError(t, os.WriteFile(filepath.Join(ws, file), []byte("x"), 0o644))
	}
	require.NoError(t, os.Symlink(filepath.Join(ws, "astro.config.mjs"), filepath.Join(ws, "src/data/link.mjs")))
	require.NoError(t, os.Symlink(filepath.Join(ws, "src/components/products"), filepath.Join(ws, "src/content/linked-products")))
	return ws
}

func TestCheckWrite(t *testing.T) {
	ws := newPolicyWorkspace(t)
	policy := PathPolicy{WritePolicy: WritePolicy{Allow: []string{"src/content/**", "src/data/**", ".agents/tmp/**"}}}

	for _, tc := range []struct {
		name, path string
		wantErr    error
	}{
		{"writable file", "src/content/news/a.md", nil},
		{"writable new file", "src/data/products.ts", nil},
		{"writable deep new file", ".agents/tmp/inbox/a.png", nil},
		{"absolute writable path", filepath.Join(ws, "src/data/x.ts"), nil},
		{"protected root file", "astro.config.mjs", assert.AnError},
		{"protected directory", "src/components/products/X.astro", assert.AnError},
		{"protected through parent segment", "src/data/../astro.config.mjs", assert.AnError},
		{"link to protected file", "src/data/link.mjs", assert.AnError},
		{"link to protected directory", "src/content/linked-products/X.astro", assert.AnError},
		{"outside workspace", "../escape.txt", ErrPathEscapesWorkspace},
		{"outside workspace absolute", "/etc/hosts", ErrPathEscapesWorkspace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := policy.CheckWrite(ws, ResolvePath(ws, tc.path))
			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tc.wantErr != assert.AnError {
				assert.ErrorIs(t, err, tc.wantErr)
			}
		})
	}
}

func TestCheckWriteWithoutPolicyKeepsWorkspaceRule(t *testing.T) {
	ws := newPolicyWorkspace(t)
	open := PathPolicy{}
	assert.NoError(t, open.CheckWrite(ws, ResolvePath(ws, "astro.config.mjs")))
	assert.ErrorIs(t, open.CheckWrite(ws, "/etc/hosts"), ErrPathEscapesWorkspace)
	assert.NoError(t, PathPolicy{AllowOutsideWorkspace: true}.CheckWrite(ws, "/etc/hosts"))
}

func TestMutationToolsRespectWritePolicy(t *testing.T) {
	ws := newPolicyWorkspace(t)
	policy := PathPolicy{WritePolicy: WritePolicy{Allow: []string{"src/content/**"}}}
	ops := operations.LocalFileOperations{}
	denied := "outside the writable paths"

	t.Run("write", func(t *testing.T) {
		tool := NewWriteTool(WithWriteWorkspace(ws), WithWritePathPolicy(policy), WithWriteOperations(ops))
		_, err := tool.Execute(t.Context(), json.RawMessage(`{"path":"astro.config.mjs","content":"pwn"}`), nil)
		require.ErrorContains(t, err, denied)
		data, readErr := os.ReadFile(filepath.Join(ws, "astro.config.mjs"))
		require.NoError(t, readErr)
		assert.Equal(t, "x", string(data))

		_, err = tool.Execute(t.Context(), json.RawMessage(`{"path":"src/content/news/b.md","content":"ok"}`), nil)
		require.NoError(t, err)
	})

	t.Run("edit", func(t *testing.T) {
		tool := NewEditTool(WithEditWorkspace(ws), WithEditPathPolicy(policy), WithEditOperations(ops))
		args, marshalErr := json.Marshal(map[string]any{"path": "src/components/products/X.astro", "old_string": "x", "new_string": "y"})
		require.NoError(t, marshalErr)
		_, err := tool.Execute(t.Context(), args, nil)
		require.ErrorContains(t, err, denied)

		args, marshalErr = json.Marshal(map[string]any{"path": "src/content/news/a.md", "old_string": "x", "new_string": "y"})
		require.NoError(t, marshalErr)
		_, err = tool.Execute(t.Context(), args, nil)
		require.NoError(t, err)
	})

	t.Run("patch", func(t *testing.T) {
		tool := NewPatchTool(WithPatchWorkspace(ws), WithPatchPathPolicy(policy), WithPatchOperations(ops))
		args, marshalErr := json.Marshal(map[string]any{"patchText": "--- a/astro.config.mjs\n+++ b/astro.config.mjs\n@@ -1 +1 @@\n-x\n+y\n"})
		require.NoError(t, marshalErr)
		_, err := tool.Execute(t.Context(), args, nil)
		require.ErrorContains(t, err, denied)
	})

	t.Run("delete_file", func(t *testing.T) {
		tool := NewDeleteFileTool(WithDeleteFileWorkspace(ws), WithDeleteFilePathPolicy(policy), WithDeleteFileOperations(ops))
		_, err := tool.Execute(t.Context(), json.RawMessage(`{"file_path":"src/components/products/X.astro"}`), nil)
		require.ErrorContains(t, err, denied)
		_, statErr := os.Stat(filepath.Join(ws, "src/components/products/X.astro"))
		assert.NoError(t, statErr)
	})
}
