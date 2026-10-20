package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hwj123hwj/easyagent/sdk/ai"
	"github.com/stretchr/testify/require"
)

func TestForkDuringRunCountsOnlyImmutableHistory(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	ctx := context.Background()
	source, err := s.resolveSession(ctx, "")
	require.NoError(t, err)
	for _, message := range []ai.Message{ai.NewTextUserMessage("first"), ai.AssistantMessage{Text: "first answer"}, ai.NewTextUserMessage("second"), ai.AssistantMessage{Text: "second answer"}} {
		require.NoError(t, source.Session().AppendMessage(ctx, message))
	}
	run, _, err := s.startRun(source.SessionID(), "live tail", "fork-snapshot-counts")
	require.NoError(t, err)
	waitRunText(t, s, run)
	fork := forkIssue(t, s, source.SessionID(), `{"before_message_index":1}`, 201)
	require.Equal(t, 2, fork.KeptMessages)
	require.Equal(t, 1, fork.KeptUserMessages)
	require.Equal(t, 1, fork.BranchUserMsgIndex)
	require.Equal(t, 2, fork.DroppedMessages, "the current prompt is outside the fork snapshot")
}

func TestForkRejectsNegativeIndexDuringRun(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	run, _, err := s.startRun("", "running", "fork-negative-index")
	require.NoError(t, err)
	waitRunText(t, s, run)
	forkIssue(t, s, run.SessionID, `{"before_message_index":-1}`, 400)
}

func TestForkDuringRunPreservesStorageErrors(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	ctx := context.Background()
	source, err := s.resolveSession(ctx, "")
	require.NoError(t, err)
	require.NoError(t, source.Session().AppendMessage(ctx, ai.NewTextUserMessage("old")))
	run, _, err := s.startRun(source.SessionID(), "running", "fork-storage-error")
	require.NoError(t, err)
	waitRunText(t, s, run)
	metaPath := filepath.Join(s.app.SessionManager().SessionsDir(), source.SessionID(), "meta.json")
	require.NoError(t, os.WriteFile(metaPath, []byte("{"), 0o600))
	// A durable-write failure must not be disguised as a temporary busy state.
	forkIssue(t, s, source.SessionID(), `{"before_message_index":0}`, 400)
	infos, err := s.app.SessionManager().List(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1, "failed persistence must clean up the new fork")
}

func TestForkUsesRuntimePrefixWithoutServerRun(t *testing.T) {
	s, _, gateway := newRunTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	source, err := s.resolveSession(ctx, "")
	require.NoError(t, err)
	history := []ai.Message{ai.NewTextUserMessage("first"), ai.AssistantMessage{Text: "first answer"}, ai.NewTextUserMessage("second"), ai.AssistantMessage{Text: "second answer"}}
	for _, message := range history {
		require.NoError(t, source.Session().AppendMessage(ctx, message))
	}
	stream, err := source.PromptStream(ctx, "SDK prompt")
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		for range stream {
		}
	})
	select {
	case <-gateway.requests:
	case <-time.After(3 * time.Second):
		t.Fatal("SDK prompt did not start")
	}
	// Automatic compaction may change live storage after the runtime captured
	// its immutable prefix. There is no server run baseline for this SDK call.
	require.NoError(t, source.Session().AppendCompaction(ctx, "live summary"))
	fork := forkIssue(t, s, source.SessionID(), `{"before_message_index":1}`, 201)
	require.Equal(t, 2, fork.KeptMessages)
	require.Equal(t, 2, fork.DroppedMessages)
	forked, _, err := s.app.SessionManager().Open(ctx, fork.ID)
	require.NoError(t, err)
	defer forked.Storage().Close()
	actual, err := forked.BuildContext(ctx)
	require.NoError(t, err)
	require.Equal(t, history[:2], actual)
}
