package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/compaction"
	"github.com/nightgopher/easyagent/sdk/session"
	"github.com/stretchr/testify/require"
)

func TestClearContextNowEmptiesLiveContextInPlace(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "context.jsonl")
	storage := session.NewJSONLStorage(path)
	require.NoError(t, storage.Init())
	sess := session.New(storage)
	require.NoError(t, sess.AppendMessage(ctx, ai.NewTextUserMessage(strings.Repeat("旧任务上下文", 50))))
	require.NoError(t, sess.AppendMessage(ctx, ai.AssistantMessage{Text: strings.Repeat("旧回复", 50)}))

	ag := New(Options{
		Session:           sess,
		Model:             ai.Model{ID: "test", ContextWindow: 1000},
		System:            "system rules",
		CompactionSettings: compaction.Settings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 350},
	})

	dropped, err := ag.ClearContextNow(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, dropped)

	after, err := sess.BuildContext(ctx)
	require.NoError(t, err)
	require.Len(t, after, 1, "清空后只剩一条边界提示")
	for _, msg := range after {
		require.NotContains(t, messageText(msg), "旧任务上下文")
	}

	// 同一会话继续使用：新消息可见，旧内容不再出现。
	require.NoError(t, sess.AppendMessage(ctx, ai.NewTextUserMessage("新任务")))
	after, err = sess.BuildContext(ctx)
	require.NoError(t, err)
	require.Len(t, after, 2)
	require.Contains(t, messageText(after[len(after)-1]), "新任务")
	require.NotContains(t, messageText(after[0]), "旧任务上下文")

	// 再次清空只是替换边界提示，仍然可用。
	dropped, err = ag.ClearContextNow(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, dropped)
}

func TestClearContextNowRejectsEmptyContext(t *testing.T) {
	ctx := context.Background()
	storage := session.NewJSONLStorage(filepath.Join(t.TempDir(), "context.jsonl"))
	require.NoError(t, storage.Init())
	sess := session.New(storage)
	ag := New(Options{Session: sess, Model: ai.Model{ID: "test", ContextWindow: 1000}})
	_, err := ag.ClearContextNow(ctx)
	require.ErrorContains(t, err, "already empty")
}

func messageText(msg ai.Message) string {
	switch m := msg.(type) {
	case ai.UserMessage:
		if m.DisplayText != "" {
			return m.DisplayText
		}
		var b strings.Builder
		for _, block := range m.Content {
			b.WriteString(block.Text)
		}
		return b.String()
	case ai.AssistantMessage:
		return m.Text
	default:
		return ""
	}
}
