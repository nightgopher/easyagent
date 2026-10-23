package server

import (
	"encoding/json"
	"net/http"

	"github.com/hwj123hwj/easyagent/sdk/agent"
)

// ─── POST /chat ───────────────────────────────────────────────────────────────

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024)).Decode(&req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	run, _, err := s.startRun(req.SessionID, req.Prompt, req.RequestID, req.Inputs)
	if err != nil {
		runHTTPError(w, err)
		return
	}
	assistant, err := s.waitRun(r.Context(), run)
	if err != nil {
		runHTTPError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ChatResponse{Text: assistant.Text, ToolCalls: assistant.ToolCalls, SessionID: run.SessionID})
}

// SSE observes the same session-owned run as WS; losing the HTTP connection only unsubscribes.
func (s *Server) chatStream(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024)).Decode(&req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	run, duplicate, err := s.startRun(req.SessionID, req.Prompt, req.RequestID, req.Inputs)
	if err != nil {
		runHTTPError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	writeSSE(w, "session_id", run.SessionID)
	writeRunSSE(w, "accepted", map[string]any{"session_id": run.SessionID, "run_id": run.ID, "request_id": run.RequestID, "duplicate": duplicate})
	observer := &wsConn{outgoing: make(chan []byte, 512), done: make(chan struct{})}
	defer observer.close()
	defer s.unsubscribeRun(observer, run.SessionID)
	snapshot, err := s.runSnapshot(run.SessionID, 0, "", observer)
	if err != nil {
		writeSSE(w, "error", err.Error())
		return
	}
	if run.restored || snapshot.Run == nil || snapshot.Run.ID != run.ID {
		// A later command/run may have replaced the session projection. Repeating
		// the old request still returns its own result rather than following a new run.
		assistant, err := s.waitRun(r.Context(), run)
		if err != nil {
			writeSSE(w, "error", err.Error())
			return
		}
		writeRunSSE(w, "done", agent.AgentStreamEvent{Type: agent.StreamEventDone, FinalMessage: assistant})
		return
	}
	if events, ok := snapshot.Events.([]agent.AgentStreamEvent); ok {
		for _, event := range events {
			writeRunSSE(w, string(event.Type), event)
		}
	}
	// A completed duplicate is replayed above; no new execution or subscription wait.
	if snapshot.Run != nil && !runActive(snapshot.Run) {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-observer.done:
			return
		case data := <-observer.outgoing:
			var event struct {
				Type         string                 `json:"type"`
				Event        agent.AgentStreamEvent `json:"event"`
				State        string                 `json:"state"`
				Confirmation *pendingConfirmation   `json:"confirmation"`
			}
			if json.Unmarshal(data, &event) != nil {
				continue
			}
			if event.Type == "event" {
				writeRunSSE(w, string(event.Event.Type), event.Event)
			}
			if event.Type == "confirmation" {
				writeRunSSE(w, "confirmation", event.Confirmation)
			}
			if event.Type == "status" && event.State != "running" && event.State != "waiting_confirmation" {
				return
			}
		}
	}
}
