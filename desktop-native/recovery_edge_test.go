package main

import (
	"encoding/json"
	"testing"
)

func TestAcceptedRunRejectsDelayedSnapshotsAndAcceptances(t *testing.T) {
	s := &session{}
	s.reduce(envelope{Type: "snapshot", Seq: 10, Run: &runInfo{ID: "old", State: "completed"}})
	s.reduce(envelope{Type: "accepted", Seq: 11, RunID: "new", State: "running", Prompt: "new prompt"})
	s.reduce(envelope{Type: "snapshot", Seq: 10, Run: &runInfo{ID: "old", State: "completed"}})
	if s.runID() != "new" || !s.Running || s.Seq != 11 {
		t.Fatalf("delayed snapshot replaced accepted run: run=%s running=%t seq=%d", s.runID(), s.Running, s.Seq)
	}
	s.reduce(envelope{Type: "replay", Seq: 10, Run: &runInfo{ID: "old", State: "completed"}})
	if s.runID() != "new" || !s.Running {
		t.Fatal("delayed replay replaced accepted run")
	}
	s.reduce(envelope{Type: "accepted", Seq: 9, RunID: "older", State: "running", Prompt: "stale"})
	if s.runID() != "new" {
		t.Fatal("stale acceptance created a phantom run")
	}
}

func TestReloadedToolArgumentsPreservePlansAndFileLinks(t *testing.T) {
	var records []storedMessage
	raw := `[{"role":"assistant","entry_id":"a","tool_calls":[{"id":"plan","name":"todo_write","args":"{\"todos\":[{\"id\":\"one\",\"content\":\"keep plan after reload\",\"status\":\"in_progress\",\"priority\":\"high\"}]}"},{"id":"read","name":"read","args":"{\"path\":\"/tmp/project/file.go\"}"}]},{"role":"tool","tool_call_id":"plan","content":"saved","entry_id":"t1"},{"role":"tool","tool_call_id":"read","content":"contents","entry_id":"t2"}]`
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		t.Fatal(err)
	}
	s := &session{}
	s.history(records)
	if len(s.Plan) != 1 || s.Plan[0].Content != "keep plan after reload" {
		t.Fatalf("plan lost after history reload: %#v", s.Plan)
	}
	if len(s.Messages) != 2 || toolFilePath(s.Messages[1]) != "/tmp/project/file.go" {
		t.Fatal("file link lost after history reload")
	}
}
