package main

import "testing"

func TestCompletedSpeechTextOnlyForNewSuccessfulRun(t *testing.T) {
	s := &session{Run: &runInfo{ID: "run-1", State: "completed"}, Messages: []*message{{User: true, Text: "question", RunID: "run-1"}, {Text: "answer", RunID: "run-1"}}}
	if got := completedSpeechText(s, true, "run-1"); got != "answer" {
		t.Fatalf("got %q", got)
	}
	s.Run.AssistantEntry = "answer-id"
	s.Messages[1].RunID, s.Messages[1].EntryID = "", "answer-id"
	if got := completedSpeechText(s, true, "run-1"); got != "answer" {
		t.Fatalf("lost completed answer after reconnect: %q", got)
	}
	if got := completedSpeechText(s, false, "run-1"); got != "" {
		t.Fatalf("replayed history: %q", got)
	}
	if got := completedSpeechText(s, true, "older"); got != "" {
		t.Fatalf("read different run: %q", got)
	}
	s.Run.State = "cancelled"
	if got := completedSpeechText(s, true, "run-1"); got != "" {
		t.Fatalf("read cancelled run: %q", got)
	}
	s.Run.State = "completed"
	s.Messages[1].Text = " "
	if got := completedSpeechText(s, true, "run-1"); got != "" {
		t.Fatalf("read empty reply: %q", got)
	}
}
