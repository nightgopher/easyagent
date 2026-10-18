package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hwj123hwj/easyagent/sdk/ai"
	"github.com/stretchr/testify/require"
)

func forkIssue(t *testing.T, s *Server, id, body string, code int) forkResponse {
	t.Helper()
	r := httptest.NewRequest("POST", "/sessions/"+id+"/fork", bytes.NewBufferString(body))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	s.forkSession(w, r)
	require.Equal(t, code, w.Code, w.Body.String())
	var result forkResponse
	if code == 201 {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	}
	return result
}
func TestHistoricalForkDuringRunAndApplicationChoice(t *testing.T) {
	s, _, gateway := newRunTestServer(t)
	ctx := context.Background()
	source, err := s.resolveSession(ctx, "")
	require.NoError(t, err)
	require.NoError(t, source.Session().AppendMessage(ctx, ai.NewTextUserMessage("old")))
	require.NoError(t, source.Session().AppendMessage(ctx, ai.AssistantMessage{Text: "old answer"}))
	ids, err := source.Session().BuildContextEntryIDs(ctx)
	require.NoError(t, err)
	run, _, err := s.startRun(source.SessionID(), "running", "fork-running")
	require.NoError(t, err)
	waitRunText(t, s, run)
	// New runs may append while each fork snapshots only the immutable prefix.
	for i := 0; i < 12; i++ {
		fork := forkIssue(t, s, source.SessionID(), fmt.Sprintf(`{"entry_id":%q,"application":"kb"}`, ids[1]), 201)
		require.Equal(t, 2, fork.KeptMessages)
		loaded, err := s.app.LoadSession(ctx, fork.ID)
		require.NoError(t, err)
		history, err := loaded.Session().BuildContext(ctx)
		require.NoError(t, err)
		require.Len(t, history, 2)
		info, err := s.app.SessionManager().List(ctx)
		require.NoError(t, err)
		found := false
		for _, item := range info {
			if item.ID == fork.ID {
				require.Equal(t, "kb", item.Application)
				found = true
			}
		}
		require.True(t, found)
	}
	forkIssue(t, s, source.SessionID(), `{}`, 409)
	forkIssue(t, s, source.SessionID(), `{"before_message_index":1}`, 409)
	forkIssue(t, s, source.SessionID(), `{"before_message_index":0}`, 201)
	tail, err := source.Session().BuildContextEntryIDs(ctx)
	require.NoError(t, err)
	forkIssue(t, s, source.SessionID(), fmt.Sprintf(`{"entry_id":%q}`, tail[len(tail)-1]), 409)
	// Full compaction during the active run must not hide the pre-run fork anchors.
	require.NoError(t, source.Session().AppendCompaction(ctx, "compacted during run"))
	fork := forkIssue(t, s, source.SessionID(), fmt.Sprintf(`{"entry_id":%q}`, ids[1]), 201)
	require.Equal(t, 2, fork.KeptMessages)
	forkIssue(t, s, source.SessionID(), `{"before_message_index":0}`, 201)
	close(gateway.finish)
	select {
	case <-run.done:
	case <-time.After(3 * time.Second):
		t.Fatal("source run was interrupted")
	}
	forkIssue(t, s, source.SessionID(), `{"application":"unknown"}`, 400)
}
func TestForkIncludesCompleteToolResultsAndPartialAnswer(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	ctx := context.Background()
	source, err := s.resolveSession(ctx, "")
	require.NoError(t, err)
	history := []ai.Message{ai.NewTextUserMessage("question"), ai.AssistantMessage{Text: "plan", ToolCalls: []ai.ToolCall{{ID: "a", Name: "read"}, {ID: "b", Name: "read"}}}, ai.ToolResultMessage{ToolCallID: "b", Content: "b result"}, ai.ToolResultMessage{ToolCallID: "a", Content: "a result"}, ai.AssistantMessage{Text: "partial", StopReason: ai.StopReasonError}}
	for _, m := range history {
		require.NoError(t, source.Session().AppendMessage(ctx, m))
	}
	ids, err := source.Session().BuildContextEntryIDs(ctx)
	require.NoError(t, err)
	for _, compact := range []bool{false, true} {
		if compact {
			require.NoError(t, source.Session().AppendCompactionKeeping(ctx, "summary", history, nil))
		}
		fork := forkIssue(t, s, source.SessionID(), fmt.Sprintf(`{"entry_id":%q}`, ids[1]), 201)
		require.Equal(t, 4, fork.KeptMessages)
		forked, _, err := s.app.SessionManager().Open(ctx, fork.ID)
		require.NoError(t, err)
		messages, err := forked.BuildContext(ctx)
		require.NoError(t, err)
		require.Equal(t, history[3], messages[len(messages)-1])
		require.NoError(t, forked.Storage().Close())
		fork = forkIssue(t, s, source.SessionID(), fmt.Sprintf(`{"entry_id":%q}`, ids[4]), 201)
		require.Equal(t, 5, fork.KeptMessages)
	}
}

func TestContinueRunUsesSavedToolResultWithoutDuplicatePrompt(t *testing.T) {
	s, _, gateway := newFileRunTestServer(t, "result.txt")
	gateway.failAfterTool.Store(true)
	run, _, err := s.startRun("", "write once", "continue-source")
	require.NoError(t, err)
	// The second LLM request fails after the real tool result was persisted.
	select {
	case <-run.done:
	case <-time.After(3 * time.Second):
		t.Fatal("failed request did not finish")
	}
	require.True(t, run.CanContinue)
	resumed, duplicate, err := s.startRun(run.SessionID, "", "resume-id", promptInputs{ContinueRunID: run.ID})
	require.NoError(t, err)
	require.False(t, duplicate)
	waitRunText(t, s, resumed)
	retry, duplicate, err := s.startRun(run.SessionID, "", "resume-id", promptInputs{ContinueRunID: run.ID})
	require.NoError(t, err)
	require.True(t, duplicate)
	require.Equal(t, resumed.ID, retry.ID)
	close(gateway.finish)
	select {
	case <-resumed.done:
	case <-time.After(3 * time.Second):
		t.Fatal("resume did not finish")
	}
	sess, err := s.resolveSession(context.Background(), run.SessionID)
	require.NoError(t, err)
	history, err := sess.Session().BuildContext(context.Background())
	require.NoError(t, err)
	users, tools := 0, 0
	for _, m := range history {
		switch m.(type) {
		case ai.UserMessage:
			users++
		case ai.ToolResultMessage:
			tools++
		}
	}
	require.Equal(t, 1, users, "continuation must not repeat the user prompt")
	require.Equal(t, 1, tools, "saved tools must not execute twice")
	require.Equal(t, int32(3), gateway.calls.Load())
	_, _, err = s.startRun(run.SessionID, "", "stale-continue", promptInputs{ContinueRunID: run.ID})
	require.Error(t, err)
}
