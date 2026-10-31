package agentdata

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublishMemoryPermissionsIdempotenceAndKnowledgeIndex(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "docs"), 0700))
	source, err := s.SaveSource(ctx, KnowledgeSource{Name: "Guide", Path: root, Directories: []string{"docs"}, Writable: true})
	require.NoError(t, err)
	a := memoryAgent(t, s)
	m, err := s.SaveMemory(ctx, a.ID, "", "user", "", Memory{Key: "日常记忆", Content: "原始事实"})
	require.NoError(t, err)
	publish := func(dir, body string, v int64) (string, error) {
		return s.PublishMemory(ctx, a.ID, m.ID, source.ID, dir, "整理笔记", body, v)
	}
	_, err = publish("docs", "整理后的知识", m.Version)
	require.Error(t, err)
	a.Knowledge = []KnowledgeBinding{{source.ID, "read"}}
	a, err = s.SaveAgent(ctx, a)
	require.NoError(t, err)
	_, err = publish("docs", "整理后的知识", m.Version)
	require.Error(t, err)
	a.Knowledge[0].Access = "write"
	_, err = s.SaveAgent(ctx, a)
	require.NoError(t, err)
	_, err = publish("../", "整理后的知识", m.Version)
	require.Error(t, err)
	_, err = publish("docs", "整理后的知识", 999)
	require.ErrorIs(t, err, ErrConflict)
	name, err := publish("docs", "整理后的知识", m.Version)
	require.NoError(t, err)
	again, err := publish("docs", "整理后的知识", m.Version)
	require.NoError(t, err)
	require.Equal(t, name, again)
	_, err = publish("docs", "changed", m.Version)
	require.ErrorIs(t, err, ErrConflict)
	body, err := s.ReadKnowledge(ctx, source.ID, name)
	require.NoError(t, err)
	require.Contains(t, body, "整理后的知识")
	require.Contains(t, body, m.ID)
	hits, err := s.SearchAgentKnowledge(ctx, a.ID, "整理后的知识")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	saved, err := s.GetMemory(ctx, a.ID, m.ID)
	require.NoError(t, err)
	require.Equal(t, "原始事实", saved.Content)
	source.Writable = false
	_, err = s.SaveSource(ctx, source)
	require.NoError(t, err)
	_, err = publish("docs", "整理后的知识", m.Version)
	require.Error(t, err)
}
func TestPublishMemoryRejectsSymlinkDestination(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	root, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "docs")))
	source, err := s.SaveSource(ctx, KnowledgeSource{Name: "source", Path: root, Directories: []string{"docs"}, Writable: true})
	require.NoError(t, err)
	a := memoryAgent(t, s)
	a.Knowledge = []KnowledgeBinding{{source.ID, "write"}}
	a, err = s.SaveAgent(ctx, a)
	require.NoError(t, err)
	m, err := s.SaveMemory(ctx, a.ID, "", "user", "", Memory{Key: "fact", Content: "fact"})
	require.NoError(t, err)
	_, err = s.PublishMemory(ctx, a.ID, m.ID, source.ID, "docs", "title", "body", m.Version)
	require.Error(t, err)
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries)
}
