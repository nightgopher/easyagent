package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Continue only reissues the LLM request against the current durable leaf. Saved tools are not re-executed.
func (s *Server) continueRunHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID     string `json:"run_id"`
		RequestID string `json:"request_id"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil || req.RunID == "" {
		writeError(w, 400, "run_id required")
		return
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, 400, "invalid continue request")
		return
	}
	run, duplicate, err := s.startRun(r.PathValue("id"), "", req.RequestID, promptInputs{ContinueRunID: req.RunID})
	if err != nil {
		runHTTPError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"run_id": run.ID, "request_id": run.RequestID, "duplicate": duplicate})
}
