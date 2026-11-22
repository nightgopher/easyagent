package dynamicflow

import (
	"encoding/json"
	"testing"
)

func TestStoppedRunReportsInterruptedNodesWithoutChangingJournal(t *testing.T) {
	original := json.RawMessage(`{"nodes":[{"status":"running","id":"a"},{"status":"completed","id":"b"}],"other":1}`)
	m := &Manager{runs: map[string]*Run{"id": {ID: "id", Status: "cancelled", State: original}}}
	view, err := m.Get("id")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Nodes []struct {
			Status string `json:"status"`
		} `json:"nodes"`
	}
	if err = json.Unmarshal(view.State, &got); err != nil {
		t.Fatal(err)
	}
	if got.Nodes[0].Status != "interrupted" || got.Nodes[1].Status != "completed" {
		t.Fatalf("wrong statuses: %+v", got.Nodes)
	}
	if string(m.runs["id"].State) != string(original) {
		t.Fatal("modified journal snapshot")
	}
	m.runs["id"].Status = "running"
	view, _ = m.Get("id")
	if string(view.State) != string(original) {
		t.Fatal("changed live state")
	}
}
