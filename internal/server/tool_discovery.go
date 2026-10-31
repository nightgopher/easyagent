package server

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/nightgopher/easyagent/sdk/config"
)

func (s *Server) getToolDiscovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.app.ToolDiscoverySettings())
}

func (s *Server) setToolDiscovery(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Mode string `json:"mode"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid tool loading settings")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "expected one JSON object")
		return
	}
	if request.Mode == "" || config.ValidateToolSearch(request.Mode) != nil {
		writeError(w, http.StatusBadRequest, "mode must be off or local")
		return
	}
	if err := s.app.SetToolDiscovery(request.Mode); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.getToolDiscovery(w, r)
}
