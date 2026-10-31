package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nightgopher/easyagent/sdk/sessionmgr"
)

func (s *Server) searchSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := sessionmgr.SearchOptions{Query: strings.TrimSpace(q.Get("q")), Workspace: q.Get("workspace"), Archived: q.Get("archived"), Limit: 30}
	valid := opts.Query != "" && utf8.ValidString(opts.Query) && utf8.RuneCountInString(opts.Query) <= 256 && !strings.ContainsRune(opts.Query, 0)
	valid = valid && (opts.Archived == "" || opts.Archived == "all" || opts.Archived == "true" || opts.Archived == "false")
	for key, target := range map[string]*int{"limit": &opts.Limit, "offset": &opts.Offset} {
		if raw := q.Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			valid = valid && err == nil
			*target = value
		}
	}
	if raw := q.Get("since"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		valid = valid && err == nil
		opts.Since = value
	}
	if !valid || opts.Limit < 1 || opts.Limit > 100 || opts.Offset < 0 || opts.Offset > 100000 || opts.Since < 0 {
		writeError(w, http.StatusBadRequest, "invalid search query or filters")
		return
	}
	results, err := s.app.SessionManager().Search(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot search session history")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(results)
}

func (s *Server) searchSessionHistory(w http.ResponseWriter, r *http.Request) {
	entryID := r.URL.Query().Get("entry_id")
	if entryID == "" || len(entryID) > 256 {
		writeError(w, http.StatusBadRequest, "entry_id is required")
		return
	}
	messages, err := s.app.SessionManager().SearchHistory(r.Context(), r.PathValue("id"), entryID)
	if errors.Is(err, sessionmgr.ErrHistoryNotFound) {
		writeError(w, http.StatusNotFound, "history message not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot read session history")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"messages": messages, "entry_id": entryID})
}
