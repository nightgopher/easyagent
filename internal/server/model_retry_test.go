package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/stretchr/testify/require"
)

func TestModelRetryKeepsSavedToolResultsAndReconnectState(t *testing.T) {
	s, server, gateway := newFileRunTestServer(t, "once.txt")
	gateway.retryStatus, gateway.retryAfter = 429, "1"
	gateway.failAfterTool.Store(true)
	run, _, err := s.startRun("", "write once and report", "auto-retry")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		s.runs.mu.Lock()
		defer s.runs.mu.Unlock()
		for _, event := range run.projection {
			if event.Type == agent.StreamEventModelRetry {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)

	// A client attaching during backoff gets progress in its normal snapshot.
	conn := dialRunWS(t, server)
	require.NoError(t, conn.WriteJSON(wsClientMessage{Type: "subscribe", SessionID: run.SessionID}))
	snapshot := readRunMessage(t, conn, "snapshot")
	require.Equal(t, "running", snapshot["run"].(map[string]any)["state"])
	var retry map[string]any
	for _, item := range snapshot["events"].([]any) {
		event := item.(map[string]any)
		if event["type"] == "model_retry" {
			retry = event["retry"].(map[string]any)
		}
	}
	require.NotNil(t, retry)
	require.EqualValues(t, 429, retry["status_code"])
	require.EqualValues(t, 1, retry["attempt"])
	require.NoError(t, conn.Close())

	// A re-send of the accepted intent must observe the same run, not start over.
	same, duplicate, err := s.startRun(run.SessionID, "write once and report", "auto-retry")
	require.NoError(t, err)
	require.True(t, duplicate)
	require.Equal(t, run.ID, same.ID)
	close(gateway.finish)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := s.waitRun(ctx, run)
	require.NoError(t, err)
	require.Equal(t, "first last", result.Text)
	require.Equal(t, "completed", run.State)
	require.EqualValues(t, 3, gateway.calls.Load())
	<-gateway.requests // tool planning request
	failed, retried := <-gateway.requests, <-gateway.requests
	require.Equal(t, failed, retried, "the retried model request must be byte-equivalent in meaning")
	last := retried["messages"].([]any)
	require.Equal(t, "tool", last[len(last)-1].(map[string]any)["role"])

	sess, err := s.resolveSession(context.Background(), run.SessionID)
	require.NoError(t, err)
	history, err := sess.Session().BuildContext(context.Background())
	require.NoError(t, err)
	users, results := 0, 0
	for _, message := range history {
		switch message.(type) {
		case ai.UserMessage:
			users++
		case ai.ToolResultMessage:
			results++
		}
	}
	require.Equal(t, 1, users)
	require.Equal(t, 1, results)
	content, err := os.ReadFile(filepath.Join(sess.Config().Workspace, "once.txt"))
	require.NoError(t, err)
	require.Equal(t, "tool-result-1", string(content))
}

func TestCancelRunDuringModelRetryDoesNotMakeAnotherRequest(t *testing.T) {
	s, _, gateway := newFileRunTestServer(t, "cancelled.txt")
	gateway.retryStatus, gateway.retryAfter = 429, "60"
	gateway.failAfterTool.Store(true)
	run, _, err := s.startRun("", "write once", "cancel-backoff")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		s.runs.mu.Lock()
		defer s.runs.mu.Unlock()
		for _, event := range run.projection {
			if event.Type == agent.StreamEventModelRetry {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)
	run.cancel()
	select {
	case <-run.done:
	case <-time.After(time.Second):
		t.Fatal("run remained stuck in retry backoff")
	}
	require.Equal(t, "cancelled", run.State)
	require.EqualValues(t, 2, gateway.calls.Load())
}
