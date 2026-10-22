package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// loopStatus is a session's watchdog loop as clients see it.
type loopStatus struct {
	Prompt          string    `json:"prompt"`
	IntervalSeconds int64     `json:"interval_seconds"`
	StartedAt       time.Time `json:"started_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	LastRunAt       time.Time `json:"last_run_at,omitzero"`
	Pending         bool      `json:"pending"`
}

// getSessionLoop reports the session's active /loop, or null. Loops are
// created with the /loop command and live in memory until stopped, expired
// or the server restarts.
func (s *Server) getSessionLoop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.app.SessionManager().Exists(id) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var status *loopStatus
	if lc := s.app.LoopManager().Get(id); lc != nil && lc.IsActive() {
		status = &loopStatus{Prompt: lc.Prompt, IntervalSeconds: int64(lc.Interval / time.Second), StartedAt: lc.StartedAt, ExpiresAt: lc.ExpiresAt, LastRunAt: lc.LastRunAt, Pending: lc.Pending}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"loop": status})
}

// deleteSessionLoop stops the session's loop. Unlike the /loop command it is
// allowed while a run is active, so a loop can be stopped mid-run.
func (s *Server) deleteSessionLoop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.app.SessionManager().Exists(id) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	stopped := s.app.LoopManager().Get(id) != nil
	s.app.LoopManager().Stop(id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"stopped": stopped})
}
