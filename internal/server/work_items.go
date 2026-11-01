package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/nightgopher/easyagent/internal/agentdata"
)

type workView struct {
	agentdata.WorkItem
	RunID          string `json:"run_id,omitempty"`
	RunState       string `json:"run_state,omitempty"`
	RunError       string `json:"run_error,omitempty"`
	Attention      string `json:"attention,omitempty"`
	SessionMissing bool   `json:"session_missing"`
}

// Runtime facts come from the run registry (restored from durable receipts on
// startup), never from a model's free-text report. They do not end the work.
func (s *Server) workView(item agentdata.WorkItem) workView {
	v := workView{WorkItem: item, SessionMissing: !s.app.SessionManager().Exists(item.SessionID)}
	s.runs.mu.Lock()
	if state := s.runs.sessions[item.SessionID]; state != nil && state.run != nil {
		v.RunID, v.RunState, v.RunError = state.run.ID, state.run.State, state.run.Error
	}
	s.runs.mu.Unlock()
	if item.Closed() {
		return v
	}
	switch {
	case v.SessionMissing:
		v.Attention = "session_missing"
	case v.RunState == "waiting_confirmation":
		v.Attention = "confirmation"
	case v.RunState == "running": // Work continues even after an earlier question/report.
	case v.RunState == "failed" || v.RunState == "interrupted" || v.RunState == "cancelled":
		v.Attention = v.RunState
	case item.State == "waiting_answer" || item.State == "waiting_review" || item.State == "blocked":
		v.Attention = item.State
	}
	return v
}

func (s *Server) workItems(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, id := s.app.Agents(), r.PathValue("work")
	if r.Method == http.MethodGet && id == "" {
		s.listWorkItems(w, r)
		return
	}
	var item agentdata.WorkItem
	var err error
	switch r.Method {
	case http.MethodGet:
		item, err = store.GetWork(r.Context(), id)
	case http.MethodPost:
		var input struct {
			SessionID string `json:"session_id"`
			Title     string `json:"title"`
		}
		if !decodeAgentJSON(w, r, &input) {
			return
		}
		// Resolve identity from persisted host metadata, including archived sessions.
		sessions, listErr := s.app.SessionManager().List(r.Context())
		if listErr != nil {
			writeError(w, 500, "cannot read sessions")
			return
		}
		found := false
		for _, session := range sessions {
			if session.ID != input.SessionID {
				continue
			}
			found = true
			item, err = store.TrackWork(r.Context(), agentdata.WorkItem{SessionID: session.ID, AgentID: session.AgentID, Application: session.Application, Workspace: session.Workspace, WorkUpdate: agentdata.WorkUpdate{Title: input.Title}})
			break
		}
		if !found {
			writeError(w, 404, "session not found")
			return
		}
	case http.MethodPut:
		var update agentdata.WorkUpdate
		if !decodeAgentJSON(w, r, &update) {
			return
		}
		item, err = store.UpdateWork(r.Context(), id, "", update)
	}
	if err != nil {
		agentDataError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost {
		w.WriteHeader(http.StatusCreated)
	}
	_ = json.NewEncoder(w).Encode(s.workView(item))
}

func (s *Server) listWorkItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	view, query := q.Get("view"), strings.TrimSpace(q.Get("q"))
	limit, offset := 30, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if raw := q.Get(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, 400, "invalid pagination")
				return
			}
			*target = n
		}
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 || (view != "" && view != "all" && view != "open" && view != "attention") {
		writeError(w, 400, "invalid work filter")
		return
	}
	items := []workView{}
	matched, more := 0, false
	for start := 0; start <= 100000; start += 100 {
		batch, err := s.app.Agents().ListWork(r.Context(), query, q.Get("session_id"), 100, start)
		if err != nil {
			agentDataError(w, err)
			return
		}
		for _, item := range batch {
			v := s.workView(item)
			if (view == "open" && item.Closed()) || (view == "attention" && v.Attention == "") {
				continue
			}
			matched++
			if matched <= offset {
				continue
			}
			if len(items) == limit {
				more = true
				break
			}
			items = append(items, v)
		}
		if more || len(batch) < 100 {
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": more, "offset": offset})
}
