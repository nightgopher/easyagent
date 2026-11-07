package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nightgopher/easyagent/internal/command"
)

// registerCommandRoutes adds personal command center endpoints.
// All endpoints are no-ops returning empty state when the store is absent
// (same convention as profile endpoints).
func (s *Server) registerCommandRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /command/state", s.commandState)
	mux.HandleFunc("POST /command/spaces", s.commandCreateSpace)
	mux.HandleFunc("PUT /command/spaces/{id}", s.commandUpdateSpace)
	mux.HandleFunc("POST /command/goals", s.commandCreateGoal)
	mux.HandleFunc("POST /command/goals/{n}/done", s.commandSetDoneItem)
	mux.HandleFunc("POST /command/goals/{n}/status", s.commandSetGoalStatus)
	mux.HandleFunc("GET /command/goals/{n}/kickoff", s.commandKickoff)
	mux.HandleFunc("POST /command/goals/{n}/dispatch", s.commandDispatch)
	mux.HandleFunc("GET /command/results", s.commandListResults)
	mux.HandleFunc("POST /command/results", s.commandAddResult)
	mux.HandleFunc("POST /command/scan", s.commandScan)
	mux.HandleFunc("POST /command/inbox/{id}/assign", s.commandInboxAssign)
	mux.HandleFunc("POST /command/inbox/{id}/done", s.commandInboxDone)
}

func (s *Server) commandStore() *command.Store { return s.app.Command() }

func (s *Server) commandState(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeJSON(w, map[string]any{"spaces": []any{}, "goals": []any{}, "inbox": []any{}})
		return
	}
	// 空切片序列化成 [] 而不是 null，方便前端直接使用
	spaces := st.Spaces()
	goals := st.Goals("")
	inbox := st.Inbox(false)
	if spaces == nil {
		spaces = []command.Space{}
	}
	if goals == nil {
		goals = []command.Goal{}
	}
	if inbox == nil {
		inbox = []command.InboxItem{}
	}
	writeJSON(w, map[string]any{
		"spaces": spaces,
		"goals":  goals,
		"inbox":  inbox,
	})
}

func (s *Server) commandCreateSpace(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeError(w, http.StatusNotImplemented, "command center is not enabled")
		return
	}
	var req struct {
		Name     string `json:"name"`
		RepoPath string `json:"repo_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	sp, err := st.CreateSpace(req.Name, req.RepoPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, sp)
}

func (s *Server) commandUpdateSpace(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeError(w, http.StatusNotImplemented, "command center is not enabled")
		return
	}
	var req struct {
		RepoPath *string `json:"repo_path"`
		Notes    *string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	id := r.PathValue("id")
	if req.RepoPath != nil {
		if err := st.SetSpaceRepo(id, *req.RepoPath); err != nil {
			writeCommandErr(w, err)
			return
		}
	}
	if req.Notes != nil {
		if err := st.SetSpaceNotes(id, *req.Notes); err != nil {
			writeCommandErr(w, err)
			return
		}
	}
	sp, err := st.Space(id)
	if err != nil {
		writeCommandErr(w, err)
		return
	}
	writeJSON(w, sp)
}

func (s *Server) commandCreateGoal(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeError(w, http.StatusNotImplemented, "command center is not enabled")
		return
	}
	var req struct {
		SpaceID     string   `json:"space_id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Done        []string `json:"done"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	g, err := st.CreateGoal(req.SpaceID, req.Title, req.Description, req.Done)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, g)
}

func goalNumberFromPath(r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func (s *Server) commandSetDoneItem(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeError(w, http.StatusNotImplemented, "command center is not enabled")
		return
	}
	n, ok := goalNumberFromPath(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid goal number")
		return
	}
	var req struct {
		Index int  `json:"index"`
		Check bool `json:"check"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := st.SetDoneItem(n, req.Index, req.Check); err != nil {
		writeCommandErr(w, err)
		return
	}
	g, _ := st.Goal(n)
	writeJSON(w, g)
}

func (s *Server) commandSetGoalStatus(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeError(w, http.StatusNotImplemented, "command center is not enabled")
		return
	}
	n, ok := goalNumberFromPath(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid goal number")
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := st.SetGoalStatus(n, req.Status); err != nil {
		writeCommandErr(w, err)
		return
	}
	g, _ := st.Goal(n)
	writeJSON(w, g)
}

func (s *Server) commandKickoff(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeError(w, http.StatusNotImplemented, "command center is not enabled")
		return
	}
	n, ok := goalNumberFromPath(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid goal number")
		return
	}
	text, err := st.Kickoff(n)
	if err != nil {
		writeCommandErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"text": text})
}

func (s *Server) commandListResults(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeJSON(w, []any{})
		return
	}
	goalNumber := -1 // all
	if v := r.URL.Query().Get("goal_number"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			goalNumber = n
		}
	}
	spaceID := r.URL.Query().Get("space_id")
	results := st.Results(spaceID, goalNumber)
	if results == nil {
		results = []command.Result{}
	}
	writeJSON(w, results)
}

func (s *Server) commandAddResult(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeError(w, http.StatusNotImplemented, "command center is not enabled")
		return
	}
	var req struct {
		SpaceID    string `json:"space_id"`
		GoalNumber int    `json:"goal_number"`
		Ref        string `json:"ref"`
		Title      string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	res, err := st.AddManualResult(req.SpaceID, req.GoalNumber, req.Ref, req.Title)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, res)
}

// commandDispatch drives the Codex desktop app through the opening moves for
// a goal: new chat → pick model → paste kickoff → send (verification A flow).
// Afterwards a short background scan loop surfaces the produced branch in the
// inbox without a manual「扫描仓库」.
func (s *Server) commandDispatch(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeError(w, http.StatusNotImplemented, "command center is not enabled")
		return
	}
	n, ok := goalNumberFromPath(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid goal number")
		return
	}
	d := command.NewCodexDispatcher()
	if d == nil {
		writeError(w, http.StatusUnprocessableEntity,
			"本机没有 agent-desktop（macOS only）。安装后设 EA_AGENT_DESKTOP 或放入 PATH/~/.local/bin")
		return
	}
	kickoff, err := st.Kickoff(n)
	if err != nil {
		writeCommandErr(w, err)
		return
	}
	logs, err := d.Dispatch(r.Context(), kickoff)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "logs": logs, "error": err.Error()})
		return
	}
	// 派发成功：后台每 20s 扫一次仓库，持续 15 分钟，结果自动进收件箱
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			st.ScanAll(ctx, command.ExecGit)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	writeJSON(w, map[string]any{"ok": true, "logs": logs})
}

func (s *Server) commandScan(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeError(w, http.StatusNotImplemented, "command center is not enabled")
		return
	}
	report := st.ScanAll(r.Context(), command.ExecGit)
	writeJSON(w, report)
}

func (s *Server) commandInboxAssign(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeError(w, http.StatusNotImplemented, "command center is not enabled")
		return
	}
	var req struct {
		GoalNumber int `json:"goal_number"` // 0 = 从收件箱标题新建目标
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	goalNumber, err := st.AssignInboxResult(r.PathValue("id"), req.GoalNumber)
	if err != nil {
		writeCommandErr(w, err)
		return
	}
	writeJSON(w, map[string]int{"goal_number": goalNumber})
}

func (s *Server) commandInboxDone(w http.ResponseWriter, r *http.Request) {
	st := s.commandStore()
	if st == nil {
		writeError(w, http.StatusNotImplemented, "command center is not enabled")
		return
	}
	if err := st.AckInbox(r.PathValue("id")); err != nil {
		writeCommandErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func writeCommandErr(w http.ResponseWriter, err error) {
	if errors.Is(err, command.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}
