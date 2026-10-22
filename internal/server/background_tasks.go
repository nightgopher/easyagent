package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	basetools "github.com/hwj123hwj/easyagent/sdk/tools"
)

// maxBackgroundTail bounds the output a client can fetch for one task.
const maxBackgroundTail = 64 << 10

// sessionBackgroundTasks returns the session's background bash tasks, or nil
// when the session or its application has none.
func (s *Server) sessionBackgroundTasks(w http.ResponseWriter, r *http.Request) (*basetools.BackgroundTasks, bool) {
	id := r.PathValue("id")
	if !s.app.SessionManager().Exists(id) {
		writeError(w, http.StatusNotFound, "session not found")
		return nil, false
	}
	sess, err := s.app.LoadSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return nil, false
	}
	holder, ok := sess.Ext().(interface {
		BackgroundTasks() *basetools.BackgroundTasks
	})
	if !ok {
		return nil, true
	}
	return holder.BackgroundTasks(), true
}

func (s *Server) listBackgroundTasks(w http.ResponseWriter, r *http.Request) {
	tasks, ok := s.sessionBackgroundTasks(w, r)
	if !ok {
		return
	}
	list := []basetools.BackgroundTaskInfo{}
	if tasks != nil {
		list = tasks.List()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tasks": list})
}

func (s *Server) getBackgroundTask(w http.ResponseWriter, r *http.Request) {
	tasks, ok := s.sessionBackgroundTasks(w, r)
	if !ok {
		return
	}
	limit := maxBackgroundTail
	if n, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && n > 0 && n < limit {
		limit = n
	}
	if tasks == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	info, output, found := tasks.Tail(r.PathValue("task"), limit)
	if !found {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"task": info, "output": output})
}

func (s *Server) stopBackgroundTask(w http.ResponseWriter, r *http.Request) {
	tasks, ok := s.sessionBackgroundTasks(w, r)
	if !ok {
		return
	}
	if tasks == nil || !tasks.Stop(r.PathValue("task")) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"stopped": true})
}
