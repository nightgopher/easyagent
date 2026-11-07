package server

import (
	"encoding/json"
	"net/http"

	"github.com/nightgopher/easyagent/internal/agentdata"
)

func (s *Server) businessTeams(w http.ResponseWriter, r *http.Request) {
	store, id := s.app.Agents(), r.PathValue("team")
	if r.Method == http.MethodGet {
		if id == "" {
			items, err := store.ListTeams(r.Context())
			if err != nil {
				agentDataError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"teams": items})
		} else {
			item, err := store.GetTeam(r.Context(), id)
			if err != nil {
				agentDataError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(item)
		}
		return
	}
	var item agentdata.Team
	if !decodeAgentJSON(w, r, &item) {
		return
	}
	if item.ID != "" && item.ID != id {
		writeError(w, 400, "team id does not match request path")
		return
	}
	item.ID = id
	saved, err := store.SaveTeam(r.Context(), item)
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
