package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nightgopher/easyagent/internal/agentdata"
)

func decodeAgentJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 96*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid configuration JSON")
		return false
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "expected one JSON object")
		return false
	}
	return true
}

func agentDataError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, agentdata.ErrNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, agentdata.ErrConflict) || errors.Is(err, agentdata.ErrDisabled) {
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}

func (s *Server) businessAgents(w http.ResponseWriter, r *http.Request) {
	store, id := s.app.Agents(), r.PathValue("agent")
	if r.Method == http.MethodGet {
		if id == "" {
			items, err := store.ListAgents(r.Context())
			if err != nil {
				agentDataError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"agents": items, "default_tools": agentdata.DefaultTools()})
		} else {
			item, err := store.GetAgent(r.Context(), id)
			if err != nil {
				agentDataError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(item)
		}
		return
	}
	var item agentdata.Agent
	if !decodeAgentJSON(w, r, &item) {
		return
	}
	if item.ID != "" && item.ID != id {
		writeError(w, 400, "agent id does not match request path")
		return
	}
	item.ID = id
	saved, err := store.SaveAgent(r.Context(), item)
	if err != nil {
		agentDataError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost {
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(saved)
}
