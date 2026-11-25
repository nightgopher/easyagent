package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nightgopher/easyagent/sdk/agent"
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
	assert.NoError(t, open.CheckWrite("", "/etc/hosts"))
	assert.NoError(t, open.CheckWrite(ws, ResolvePath(ws, "src/data/link.mjs")))
	require.NoError(t, os.Link(filepath.Join(ws, "astro.config.mjs"), filepath.Join(ws, "hardlink.mjs")))
	assert.NoError(t, open.CheckWrite(ws, ResolvePath(ws, "hardlink.mjs")))
}

func TestConfiguredWritePolicyRequiresWorkspace(t *testing.T) {
	ws := t.TempDir()
	file := filepath.Join(ws, "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	dangling := filepath.Join(ws, "dangling")
	require.NoError(t, os.Symlink(filepath.Join(ws, "missing"), dangling))
	for _, workspace := range []string{"", ".", "relative/workspace", filepath.Join(ws, "missing"), file, dangling} {
		t.Run(workspace, func(t *testing.T) {
			for _, policy := range []WritePolicy{{Allow: []string{"**"}}, {Deny: []string{"protected/**"}}} {
				// -y sets AllowOutsideWorkspace; it must not disable a configured policy.
				p := PathPolicy{AllowOutsideWorkspace: true, WritePolicy: policy}
				assert.Error(t, p.CheckWrite(workspace, file))
			}
		})
	}
}

func TestConfiguredWritePolicyRejectsLinks(t *testing.T) {
	ws := t.TempDir()
	for _, dir := range []string{"allowed", "protected"} {
		require.NoError(t, os.MkdirAll(filepath.Join(ws, dir), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(ws, dir, "file"), []byte("x"), 0o644))
	}
	for _, link := range []struct{ name, target string }{
		{"allowed/to-protected", "protected/file"},
		{"protected/to-allowed", "allowed/file"},
		{"allowed/to-allowed", "allowed/file"},
		{"allowed/dir", "allowed"},
		{"allowed/dangling", "missing"},
	} {
		require.NoError(t, os.Symlink(filepath.Join(ws, link.target), filepath.Join(ws, link.name)))
	}
	// A hardlink to a protected file carries no symlink metadata.
	require.NoError(t, os.Link(filepath.Join(ws, "protected/file"), filepath.Join(ws, "allowed/hardlink")))
	// A workspace-only inode scan would miss this alias outside the workspace.
	outside := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.Link(filepath.Join(ws, "allowed/file"), outside))
	for _, allowOutside := range []bool{false, true} {
		p := PathPolicy{AllowOutsideWorkspace: allowOutside, WritePolicy: WritePolicy{
			Allow: []string{"**"}, Deny: []string{"protected/**"},
		}}
		for _, name := range []string{
			"allowed/to-protected", "protected/to-allowed", "allowed/to-allowed",
			"allowed/dir/new/deep/file", "allowed/dangling", "allowed/dangling/new/deep/file",
			"allowed/hardlink", "allowed/file",
		} {
			t.Run(name, func(t *testing.T) {
				assert.Error(t, p.CheckWrite(ws, ResolvePath(ws, name)))
			})
		}
		assert.ErrorContains(t, p.CheckWrite(ws, ResolvePath(ws, "protected/to-allowed")), "protected pattern")
		assert.ErrorContains(t, p.CheckWrite(ws, ResolvePath(ws, "allowed/to-protected")), "protected pattern")
		assert.ErrorIs(t, p.CheckWrite(ws, outside), ErrPathEscapesWorkspace)
		assert.NoError(t, p.CheckWrite(ws, ResolvePath(ws, "allowed/new/deep/file")))
	}
}

func TestConfiguredWritePolicyWorkspaceAlias(t *testing.T) {
	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "file"), []byte("x"), 0o644))
	alias := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.Symlink(ws, alias))
	p := PathPolicy{WritePolicy: WritePolicy{Allow: []string{"**"}}}
	// Resolve the workspace itself (including macOS /var aliases), but reject
	// symlink traversal beneath it.
	assert.NoError(t, p.CheckWrite(alias, ResolvePath(alias, "file")))
	assert.NoError(t, p.CheckWrite(alias, ResolvePath(alias, "new/deep/file")))
}

func TestMutationToolsConfiguredPolicyWithoutConfirmation(t *testing.T) {
	ws := newPolicyWorkspace(t)
	require.NoError(t, os.Symlink(filepath.Join(ws, "src/content/news/a.md"), filepath.Join(ws, "src/content/news/alias.md")))
	require.NoError(t, os.Symlink(filepath.Join(ws, "missing"), filepath.Join(ws, "src/content/news/dangling")))
	require.NoError(t, os.Link(filepath.Join(ws, "astro.config.mjs"), filepath.Join(ws, "src/content/news/hardlink.md")))
	outside := filepath.Join(t.TempDir(), "outside.md")
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o644))
	policy := PathPolicy{AllowOutsideWorkspace: true, WritePolicy: WritePolicy{Allow: []string{"src/content/**"}}}
	for _, tc := range []struct{ name, workspace, path, denied string }{
		{"empty workspace", "", filepath.Join(ws, "src/content/news/a.md"), "absolute workspace"},
		{"protected", ws, "astro.config.mjs", "outside the writable paths"},
		{"outside", ws, outside, "path escapes workspace"},
		{"symlink", ws, "src/content/news/alias.md", "traverses symlink"},
		{"hardlink", ws, "src/content/news/hardlink.md", "multiple hardlinks"},
		{"dangling intermediate", ws, "src/content/news/dangling/new/deep/file.md", "cannot resolve mutation path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := []struct {
				tool agent.Tool
				args map[string]any
			}{
				{NewWriteTool(WithWriteWorkspace(tc.workspace), WithWritePathPolicy(policy)), map[string]any{"path": tc.path, "content": "pwn"}},
				{NewEditTool(WithEditWorkspace(tc.workspace), WithEditPathPolicy(policy)), map[string]any{"path": tc.path, "old_string": "x", "new_string": "pwn"}},
				{NewPatchTool(WithPatchWorkspace(tc.workspace), WithPatchPathPolicy(policy)), map[string]any{"patchText": "--- " + tc.path + "\n+++ " + tc.path + "\n@@ -1 +1 @@\n-x\n+pwn\n"}},
				{NewDeleteFileTool(WithDeleteFileWorkspace(tc.workspace), WithDeleteFilePathPolicy(policy)), map[string]any{"file_path": tc.path}},
			}
			for _, tt := range tools {
				t.Run(tt.tool.Name(), func(t *testing.T) {
					raw, err := json.Marshal(tt.args)
					require.NoError(t, err)
					// Execute directly, without the confirmation gate, as in auto-
					// approved (-y) calls. Policy enforcement must remain mandatory.
					result, err := tt.tool.Execute(t.Context(), raw, nil)
					require.ErrorContains(t, err, tc.denied)
					assert.True(t, result.IsError)
				})
			}
		})
	}
	for _, file := range []string{filepath.Join(ws, "astro.config.mjs"), filepath.Join(ws, "src/content/news/a.md"), outside} {
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		assert.Equal(t, "x", string(data))
	}
	_, err := os.Lstat(filepath.Join(ws, "src/content/news/alias.md"))
	assert.NoError(t, err, "delete must not remove symlinks")
	_, err = os.Stat(filepath.Join(ws, "missing"))
	assert.True(t, os.IsNotExist(err), "must not create the dangling link's target")
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
