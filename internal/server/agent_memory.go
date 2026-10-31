package server

import (
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/nightgopher/easyagent/internal/agentdata"
)

func (s *Server) agentMemory(w http.ResponseWriter, r *http.Request) {
	store, agentID, id := s.app.Agents(), r.PathValue("agent"), r.PathValue("memory")
	a, err := store.GetAgent(r.Context(), agentID)
	if err != nil {
		agentDataError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		if id != "" {
			m, err := store.GetMemory(r.Context(), agentID, id)
			if err != nil {
				agentDataError(w, err)
				return
			}
			writeJSON(w, m)
			return
		}
		offset := 0
		if raw := r.URL.Query().Get("offset"); raw != "" {
			offset, err = strconv.Atoi(raw)
			if err != nil || offset < 0 {
				writeError(w, 400, "invalid offset")
				return
			}
		}
		items, more, err := store.ListMemories(r.Context(), agentID, r.URL.Query().Get("q"), offset)
		if err != nil {
			agentDataError(w, err)
			return
		}
		writeJSON(w, map[string]any{"memories": items, "enabled": a.MemoryEnabled, "has_more": more, "offset": offset})
		return
	}
	if r.Method == http.MethodDelete {
		var input struct {
			Version int64 `json:"version"`
		}
		if !decodeAgentJSON(w, r, &input) {
			return
		}
		if err := store.DeleteMemory(r.Context(), agentID, id, input.Version); err != nil {
			agentDataError(w, err)
			return
		}
		writeJSON(w, map[string]bool{"deleted": true})
		return
	}
	var input agentdata.Memory
	if !decodeAgentJSON(w, r, &input) {
		return
	}
	if input.ID != "" && input.ID != id {
		writeError(w, 400, "memory id does not match request path")
		return
	}
	input.ID = id
	workspace, err := filepath.Abs(store.Workspace(a))
	if err != nil {
		agentDataError(w, err)
		return
	}
	m, err := store.SaveMemory(r.Context(), agentID, workspace, "user", "", input)
	if err != nil {
		agentDataError(w, err)
		return
	}
	writeJSON(w, m)
}
