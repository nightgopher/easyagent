package session

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSession_PersistenceAcrossReload 验证会话持久化后重载能恢复完整历史。
func TestSession_PersistenceAcrossReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	ctx := context.Background()

	// 第一次写入
	storage1 := NewJSONLStorage(path)
	require.NoError(t, storage1.Init())
	sess1 := New(storage1)
	require.NoError(t, sess1.AppendMessage(ctx, ai.NewTextUserMessage("first")))
	require.NoError(t, sess1.AppendMessage(ctx, ai.AssistantMessage{Text: "second"}))
	require.NoError(t, storage1.Close())

	// 重新加载
	storage2 := NewJSONLStorage(path)
	require.NoError(t, storage2.Init())
	defer storage2.Close()
	sess2 := New(storage2)
	require.NoError(t, sess2.InitFromStorage(ctx))

	messages, err := sess2.BuildContext(ctx)
	require.NoError(t, err)
	assert.Len(t, messages, 2)
	assert.Equal(t, ai.RoleUser, messages[0].Role())
	assert.Equal(t, ai.RoleAssistant, messages[1].Role())
}

// TestSession_Compaction_LastOperation_PersistenceAcrossReload 验证
// compaction 作为最后一个操作时，重载后 leaf 指针正确恢复到 compaction entry，
// BuildContext 能看到摘要（而非旧消息）。
func TestSession_Compaction_LastOperation_PersistenceAcrossReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	ctx := context.Background()

	// 第一次写入：消息 → 压缩（压缩是最后一步）
	storage1 := NewJSONLStorage(path)
	require.NoError(t, storage1.Init())
	sess1 := New(storage1)
	require.NoError(t, sess1.AppendMessage(ctx, ai.NewTextUserMessage("old msg 1")))
	require.NoError(t, sess1.AppendMessage(ctx, ai.AssistantMessage{Text: "old resp 1"}))
	require.NoError(t, sess1.AppendMessage(ctx, ai.NewTextUserMessage("old msg 2")))
	require.NoError(t, sess1.AppendMessage(ctx, ai.AssistantMessage{Text: "old resp 2"}))
	// compact 是最后一个操作
	require.NoError(t, sess1.AppendCompaction(ctx, "Summary of old conversation"))
	require.NoError(t, storage1.Close())

	// 重新加载 — 验证 leaf 指向 compaction entry
	storage2 := NewJSONLStorage(path)
	require.NoError(t, storage2.Init())
	defer storage2.Close()
	sess2 := New(storage2)
	require.NoError(t, sess2.InitFromStorage(ctx))

	// BuildContext 应只返回 summary 消息（无旧消息）
	messages, err := sess2.BuildContext(ctx)
	require.NoError(t, err)
	assert.Len(t, messages, 1)
	assert.Equal(t, ai.RoleUser, messages[0].Role())
	userMsg, ok := messages[0].(ai.UserMessage)
	require.True(t, ok)
	assert.Contains(t, userMsg.Content[0].Text, "Summary of old conversation")
	// 确保旧消息不在
	assert.NotContains(t, userMsg.Content[0].Text, "old msg 1")
	assert.NotContains(t, userMsg.Content[0].Text, "old resp")
}

// TestJSONLStorage_Fork 验证 Fork 创建了独立的分支存储，
// 可以独立追加消息而不影响父分支。
func TestJSONLStorage_Fork(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	ctx := context.Background()

	// 创建原始存储并写入消息
	storage := NewJSONLStorage(path)
	require.NoError(t, storage.Init())
	defer storage.Close()

	userMsg := ai.NewTextUserMessage("hello")
	require.NoError(t, storage.Append(ctx, Entry{Type: EntryTypeMessage, User: &userMsg}))
	var entry1ID string
	for id := range storage.byID {
		entry1ID = id
	}

	assistantMsg := ai.AssistantMessage{Text: "world"}
	require.NoError(t, storage.Append(ctx, Entry{Type: EntryTypeMessage, Assistant: &assistantMsg, ParentID: entry1ID}))
	var entry2ID string
	for id, e := range storage.byID {
		if e.Assistant != nil {
			entry2ID = id
		}
	}
	require.NoError(t, storage.SetLeaf(ctx, entry2ID))

	// Fork 到 entry1（在 user message 处分叉）
	forked, err := storage.Fork(ctx, entry1ID)
	require.NoError(t, err)
	defer forked.Close()

	// forked 存储应该能看到分叉点的 entry
	pathOnFork, err := forked.GetPathToRoot(ctx, "")
	require.NoError(t, err)
	assert.Len(t, pathOnFork, 1)
	assert.Equal(t, "hello", pathOnFork[0].User.Content[0].Text)

	// 在 fork 上追加一条不同的消息
	forkAssistantMsg := ai.AssistantMessage{Text: "forked world"}
	require.NoError(t, forked.Append(ctx, Entry{Type: EntryTypeMessage, Assistant: &forkAssistantMsg, ParentID: entry1ID}))
	var forkedEntryID string
	for id, e := range forked.(*JSONLStorage).byID {
		if e.Assistant != nil && e.Assistant.Text == "forked world" {
			forkedEntryID = id
		}
	}
	require.NoError(t, forked.SetLeaf(ctx, forkedEntryID))

	// fork 的路径应该包含自己的消息
	forkPath, err := forked.GetPathToRoot(ctx, "")
	require.NoError(t, err)
	assert.Len(t, forkPath, 2)
	assert.Equal(t, "forked world", forkPath[1].Assistant.Text)

	// 父存储不受影响
	parentPath, err := storage.GetPathToRoot(ctx, "")
	require.NoError(t, err)
	assert.Len(t, parentPath, 2)
	assert.Equal(t, "world", parentPath[1].Assistant.Text)
}

// Simulate SIGTERM after the message fsync and before the separate leaf write.
func TestReloadRecoversDurableToolResultWithoutLeaf(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	store := NewJSONLStorage(path)
	require.NoError(t, store.Init())
	require.NoError(t, store.Append(ctx, Entry{ID: "call", Type: EntryTypeMessage, Assistant: &ai.AssistantMessage{Text: "running"}}))
	require.NoError(t, store.SetLeaf(ctx, "call"))
	require.NoError(t, store.Append(ctx, Entry{ID: "result", ParentID: "call", Type: EntryTypeMessage, Tool: &ai.ToolResultMessage{ToolCallID: "tc", Content: "command canceled: context canceled", IsError: true}}))
	require.NoError(t, store.Close())
	restored := NewJSONLStorage(path)
	require.NoError(t, restored.Init())
	defer restored.Close()
	leaf, err := restored.GetLeaf(ctx)
	require.NoError(t, err)
	require.Equal(t, "result", leaf)
	entries, err := restored.GetPathToRoot(ctx, "")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.True(t, entries[1].Tool.IsError)
	// An explicit branch navigation must still override automatic cursor recovery.
	require.NoError(t, restored.SetLeaf(ctx, "call"))
	require.NoError(t, restored.Close())
	again := NewJSONLStorage(path)
	require.NoError(t, again.Init())
	defer again.Close()
	leaf, err = again.GetLeaf(ctx)
	require.NoError(t, err)
	require.Equal(t, "call", leaf)
}

func TestToolImagePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "images.jsonl")
	storage := NewJSONLStorage(path)
	require.NoError(t, storage.Init())
	tool := ai.ToolResultMessage{ToolCallID: "shot", Content: "screenshot", Images: []ai.ImageBlock{{MediaType: "image/png", Data: []byte{1, 2, 3}}}}
	require.NoError(t, storage.Append(context.Background(), Entry{Type: EntryTypeMessage, Tool: &tool}))
	require.NoError(t, storage.Close())
	reopened := NewJSONLStorage(path)
	require.NoError(t, reopened.Init())
	defer reopened.Close()
	entries, err := reopened.GetPathToRoot(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, tool.Images, entries[0].Tool.Images)
}
