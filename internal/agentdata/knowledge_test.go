package agentdata

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKnowledgeIndexTracksEditsDeletionAndBindings(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	root := t.TempDir()
	name := filepath.Join(root, "lesson.md")
	require.NoError(t, os.WriteFile(name, []byte("# 网关经验\n断线重连需要退避。"), 0600))
	source, err := s.SaveSource(ctx, KnowledgeSource{Name: "经验库", Path: root})
	require.NoError(t, err)
	a, err := s.SaveAgent(ctx, Agent{Name: "Support", Instructions: "Role", Knowledge: []KnowledgeBinding{{SourceID: source.ID, Access: "read"}}})
	require.NoError(t, err)
	b, err := s.SaveAgent(ctx, Agent{Name: "Other", Instructions: "Role"})
	require.NoError(t, err)
	for _, query := range []string{"断线重连", "网关", "", "退"} {
		hits, err := s.SearchAgentKnowledge(ctx, a.ID, query)
		require.NoError(t, err)
		require.Len(t, hits, 1)
		require.Equal(t, source.ID, hits[0].SourceID)
		require.Equal(t, "lesson.md", hits[0].Path)
	}
	hits, err := s.SearchAgentKnowledge(ctx, b.ID, "网关")
	require.NoError(t, err)
	require.Empty(t, hits)
	_, err = s.AgentSource(ctx, b.ID, source.ID, false)
	require.Error(t, err)
	_, err = s.AgentSource(ctx, a.ID, source.ID, true)
	require.Error(t, err)
	require.NoError(t, os.WriteFile(name, []byte("# 新规则\n改用指数退避并限制次数。"), 0600))
	hits, err = s.SearchKnowledge(ctx, source.ID, "断线重连", 10)
	require.NoError(t, err)
	require.Empty(t, hits)
	hits, err = s.SearchKnowledge(ctx, source.ID, "指数退避", 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	content, err := s.ReadKnowledge(ctx, source.ID, "lesson.md")
	require.NoError(t, err)
	require.Contains(t, content, "指数退避")
	require.NoError(t, os.Remove(name))
	hits, err = s.SearchKnowledge(ctx, source.ID, "指数退避", 10)
	require.NoError(t, err)
	require.Empty(t, hits)
	source.Disabled = true
	_, err = s.SaveSource(ctx, source)
	require.NoError(t, err)
	_, err = s.SearchAgentKnowledge(ctx, a.ID, "")
	require.Error(t, err)
}

func TestKnowledgeSourcesExcludePrivateAndEscapingPaths(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	root := t.TempDir()
	for _, dir := range []string{"docs", "private", ".hidden", "chatgpt-export"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, dir), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(root, dir, "note.md"), []byte("# Test\nsource secret"), 0600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "docs", "AGENTS.md"), []byte("private instructions"), 0600))
	require.NoError(t, os.Symlink(filepath.Join(root, "private", "note.md"), filepath.Join(root, "docs", "link.md")))
	source, err := s.SaveSource(ctx, KnowledgeSource{Name: "Docs", Path: root, Directories: []string{"docs"}})
	require.NoError(t, err)
	hits, err := s.SearchKnowledge(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	for _, name := range []string{"../outside.md", "private/note.md", "docs/AGENTS.md", "docs/link.md", ".hidden/note.md"} {
		_, err := s.ReadKnowledge(ctx, source.ID, name)
		require.Error(t, err, name)
	}
	_, err = s.SaveSource(ctx, KnowledgeSource{Name: "bad", Path: root, Directories: []string{"../private"}})
	require.Error(t, err)
	source.Path = filepath.Join(root, "missing")
	_, err = s.SaveSource(ctx, source)
	require.NoError(t, err)
	_, err = s.SearchKnowledge(ctx, source.ID, "", 10)
	require.Error(t, err, "missing roots must not silently return cached results")
}

func TestLegacySourceRegistrationIsNonDestructive(t *testing.T) {
	s := New(t.TempDir())
	ctx := context.Background()
	require.NoError(t, s.EnsureLegacySource(ctx, t.TempDir()))
	source, err := s.GetSource(ctx, "legacy-agent-lessons")
	require.NoError(t, err)
	require.False(t, source.Writable)
	source.Name = "My collection"
	source, err = s.SaveSource(ctx, source)
	require.NoError(t, err)
	require.NoError(t, s.EnsureLegacySource(ctx, t.TempDir()))
	stored, err := s.GetSource(ctx, source.ID)
	require.NoError(t, err)
	require.Equal(t, source, stored)
	_, err = s.SaveSource(ctx, KnowledgeSource{ID: source.ID, Name: "stale", Path: source.Path, Version: 1})
	require.ErrorIs(t, err, ErrConflict)
}
