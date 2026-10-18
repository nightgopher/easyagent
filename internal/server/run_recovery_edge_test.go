package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hwj123hwj/easyagent/internal/app"
	"github.com/hwj123hwj/easyagent/sdk/ai"
	"github.com/hwj123hwj/easyagent/sdk/session"
	"github.com/stretchr/testify/require"
)

func TestRestartCanResumeDurableToolTail(t *testing.T) {
	for _, snapshotFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot_first=%t", snapshotFirst), func(t *testing.T) {
			first, _, gateway := newFileRunTestServer(t, "effect.txt")
			gateway.failAfterTool.Store(true)
			run, _, err := first.startRun("", "write once", "before-crash")
			require.NoError(t, err)
			select {
			case <-run.done:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not fail")
			}
			require.True(t, run.CanContinue)
			// Only the acceptance receipt was saved before a process crash. The tool tail is durable.
			require.NoError(t, first.saveRunReceipt(&sessionRun{baseEntryID: run.baseEntryID, ID: run.ID, RequestID: run.RequestID, SessionID: run.SessionID, Prompt: run.Prompt, State: "running", StartedAt: run.StartedAt}))
			cfg := first.app.Config()
			first.cancel()
			require.NoError(t, first.app.Close())
			nextApp, err := app.New(app.AppOptions{Config: cfg})
			require.NoError(t, err)
			next := New(nextApp, nil)
			t.Cleanup(func() { next.cancel(); nextApp.Close() })
			if snapshotFirst {
				snapshot, err := next.runSnapshot(run.SessionID, 0, "", nil)
				require.NoError(t, err)
				require.True(t, snapshot.Run.CanContinue, "the first restored snapshot must expose continuation")
			}
			body := fmt.Sprintf(`{"run_id":%q,"request_id":"after-crash"}`, run.ID)
			req := httptest.NewRequest("POST", "/", strings.NewReader(body))
			req.SetPathValue("id", run.SessionID)
			rec := httptest.NewRecorder()
			next.continueRunHTTP(rec, req)
			require.Equal(t, 202, rec.Code, rec.Body.String())
			var accepted struct {
				ID string `json:"run_id"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &accepted))
			require.NotEmpty(t, accepted.ID)
			close(gateway.finish)
			next.runs.mu.Lock()
			resumed := next.runs.requests["after-crash"]
			next.runs.mu.Unlock()
			_, err = next.waitRun(context.Background(), resumed)
			require.NoError(t, err)
			sess, err := next.resolveSession(context.Background(), run.SessionID)
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
			require.Equal(t, 1, users)
			require.Equal(t, 1, tools)
			require.EqualValues(t, 3, gateway.calls.Load())
		})
	}
}

func TestRestoredToolTailRequiresCompleteResultsFromThisRun(t *testing.T) {
	call := ai.AssistantMessage{ToolCalls: []ai.ToolCall{{ID: "first", Name: "write", Args: "{}"}, {ID: "second", Name: "write", Args: "{}"}}}
	result := ai.ToolResultMessage{ToolCallID: "first", Content: "saved"}
	otherResult := ai.ToolResultMessage{ToolCallID: "second", Content: "saved"}
	ptr := func(s string) *string { return &s }
	for _, tc := range []struct {
		name     string
		base     *string
		messages []ai.Message
		inputs   promptInputs
		started  int64
		prompt   string
		want     bool
	}{
		{name: "complete new tail", base: ptr(""), messages: []ai.Message{call, result, otherResult}, want: true},
		{name: "missing parallel result", base: ptr(""), messages: []ai.Message{call, result}},
		{name: "partial assistant already saved", base: ptr(""), messages: []ai.Message{call, result, otherResult, ai.AssistantMessage{Text: "partial"}}},
		{name: "old tail predates admission", base: ptr("2"), messages: []ai.Message{call, result, otherResult}},
		{name: "continuation can retain original tail", base: ptr("2"), inputs: promptInputs{ContinueRunID: "previous"}, messages: []ai.Message{call, result, otherResult}, want: true},
		{name: "missing cursor", base: ptr("not-in-path"), messages: []ai.Message{call, result, otherResult}},
		{name: "legacy new tail", started: 999, prompt: "same prompt", messages: []ai.Message{ai.NewTextUserMessage("same prompt"), call, result, otherResult}, want: true},
		{name: "legacy old tail with same prompt", started: 1000, prompt: "same prompt", messages: []ai.Message{ai.NewTextUserMessage("same prompt"), call, result, otherResult}},
		{name: "legacy unrelated prompt", started: 999, prompt: "different", messages: []ai.Message{ai.NewTextUserMessage("same prompt"), call, result, otherResult}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			storage := session.NewJSONLStorage(filepath.Join(t.TempDir(), "session.jsonl"))
			require.NoError(t, storage.Init())
			t.Cleanup(func() { storage.Close() })
			parent := ""
			for i, message := range tc.messages {
				entry := session.Entry{ID: fmt.Sprint(i), ParentID: parent, Timestamp: 1000, Type: session.EntryTypeMessage}
				switch m := message.(type) {
				case ai.UserMessage:
					entry.User = &m
				case ai.AssistantMessage:
					entry.Assistant = &m
				case ai.ToolResultMessage:
					entry.Tool = &m
				}
				require.NoError(t, storage.Append(ctx, entry))
				parent = entry.ID
			}
			run := &sessionRun{baseEntryID: tc.base, Inputs: tc.inputs, Prompt: tc.prompt, StartedAt: time.UnixMilli(tc.started)}
			got, err := restoredToolTail(ctx, run, session.New(storage))
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
