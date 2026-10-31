package server

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/nightgopher/easyagent/internal/agentdata"
)

func (s *Server) knowledgeSources(w http.ResponseWriter, r *http.Request) {
	store, id := s.app.Agents(), r.PathValue("source")
	if err := store.EnsureLegacySource(r.Context(), s.resolveKBRepoPath()); err != nil {
		agentDataError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		if id != "" {
			source, err := store.GetSource(r.Context(), id)
			if err != nil {
				agentDataError(w, err)
				return
			}
			writeJSON(w, source)
			return
		}
		items, err := store.ListSources(r.Context())
		if err != nil {
			agentDataError(w, err)
			return
		}
		type sourceStatus struct {
			agentdata.KnowledgeSource
			Available bool   `json:"available"`
			Problem   string `json:"problem,omitempty"`
		}
		result := []sourceStatus{}
		for _, source := range items {
			item := sourceStatus{KnowledgeSource: source}
			info, err := os.Stat(source.Path)
			if err != nil || !info.IsDir() {
				item.Problem = "当前主机无法读取此目录"
			} else {
				item.Available = true
			}
			result = append(result, item)
		}
		writeJSON(w, map[string]any{"sources": result})
		return
	}
	var source agentdata.KnowledgeSource
	if !decodeAgentJSON(w, r, &source) {
		return
	}
	if source.ID != "" && source.ID != id {
		writeError(w, 400, "source id does not match request path")
		return
	}
	source.ID = id
	saved, err := store.SaveSource(r.Context(), source)
	if err != nil {
		agentDataError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost {
		w.WriteHeader(201)
	}
	_ = json.NewEncoder(w).Encode(saved)
}

func (s *Server) knowledgeSearch(w http.ResponseWriter, r *http.Request) {
	store := s.app.Agents()
	var hits []agentdata.KnowledgeHit
	var err error
	if id := r.PathValue("agent"); id != "" {
		hits, err = store.SearchAgentKnowledge(r.Context(), id, r.URL.Query().Get("q"))
	} else {
		hits, err = store.SearchKnowledge(r.Context(), r.PathValue("source"), r.URL.Query().Get("q"), 50)
	}
	if err != nil {
		agentDataError(w, err)
		return
	}
	writeJSON(w, map[string]any{"entries": hits})
}

func (s *Server) knowledgeRead(w http.ResponseWriter, r *http.Request) {
	store, id := s.app.Agents(), r.PathValue("source")
	if agentID := r.PathValue("agent"); agentID != "" {
		if _, err := store.AgentSource(r.Context(), agentID, id, false); err != nil {
			agentDataError(w, err)
			return
		}
	}
	content, err := store.ReadKnowledge(r.Context(), id, r.URL.Query().Get("path"))
	if err != nil {
		agentDataError(w, err)
		return
	}
	writeJSON(w, map[string]any{"source_id": id, "path": r.URL.Query().Get("path"), "content": content})
}

func (s *Server) knowledgeReindex(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Agents().IndexSource(r.Context(), r.PathValue("source")); err != nil {
		agentDataError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
