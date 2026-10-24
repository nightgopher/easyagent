package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/hwj123hwj/easyagent/sdk/agent"
	"github.com/hwj123hwj/easyagent/sdk/runtime"
	"github.com/hwj123hwj/easyagent/sdk/sessionmgr"
	"github.com/hwj123hwj/easyagent/sdk/slashcmd"
)

// ─── GET /sessions ────────────────────────────────────────────────────────────

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	mgr := s.app.SessionManager()
	sessions, err := mgr.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Live run state lets clients mark sessions that are working or waiting for
	// approval without opening each one. Fields are omitted for idle sessions.
	type listedSession struct {
		sessionmgr.SessionInfo
		RunState             string `json:"run_state,omitempty"`
		PendingConfirmations int    `json:"pending_confirmations,omitempty"`
	}
	listed := make([]listedSession, len(sessions))
	s.runs.mu.Lock()
	for i, info := range sessions {
		listed[i] = listedSession{SessionInfo: info}
		if state := s.runs.sessions[info.ID]; state != nil && state.run != nil && runActive(state.run) {
			listed[i].RunState = state.run.State
			listed[i].PendingConfirmations = len(state.run.pending)
		}
	}
	s.runs.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(listed)
}

// ─── POST /sessions ───────────────────────────────────────────────────────────

// CreateSessionRequest is the request body for creating a session.
type CreateSessionRequest struct {
	Cwd         string `json:"cwd,omitempty"`
	Model       string `json:"model,omitempty"`
	Application string `json:"application,omitempty"` // e.g. "coding", "music"
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	// Allow empty body
	_ = json.NewDecoder(r.Body).Decode(&req)

	cfg := s.app.Config()

	// Apply cwd override if provided
	if req.Cwd != "" {
		cfg.Workspace = req.Cwd
	}

	// Apply model override if provided
	if req.Model != "" {
		switch cfg.Provider {
		case "openai":
			cfg.OpenAIModel = req.Model
		case "anthropic":
			cfg.AnthropicModel = req.Model
		}
	}

	deps := s.app.SessionDepsWithApp(req.Application)
	opts := runtime.AgentSessionOptions{
		Config: cfg,
	}
	sess, err := s.app.SessionStore().Create(r.Context(), opts, deps)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Persist the resolved workspace, including sessions using the server default.
	if err := s.app.SessionManager().SaveMeta(sess.SessionID(), sess.Workspace(), req.Application); err != nil {
		_ = s.app.SessionStore().Delete(sess.SessionID())
		writeError(w, http.StatusInternalServerError, "cannot save session metadata")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SessionResponse{
		ID:        sess.SessionID(),
		CreatedAt: time.Now().Unix(),
	})
}

// ─── GET /sessions/{id}/messages ──────────────────────────────────────────────

func (s *Server) getSessionMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	// Check if session exists in the session manager
	if !s.app.SessionManager().Exists(sessionID) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	sess, err := s.app.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	messages, err := sess.Session().BuildDisplayContext(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(serializeSessionContext(sess, messages))
}

// ─── DELETE /sessions/{id} ────────────────────────────────────────────────────

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	s.runs.mu.Lock()
	defer s.runs.mu.Unlock()
	if state := s.runs.sessions[sessionID]; sessionBusy(state) {
		writeError(w, http.StatusConflict, "会话仍在执行任务，请先取消或等待完成")
		return
	}
	if sess, ok := s.app.SessionStore().Get(sessionID); ok && sess.IsBusy() {
		writeError(w, http.StatusConflict, "会话仍在执行另一入口的任务，请先取消或等待完成")
		return
	}
	mgr := s.app.SessionManager()
	if err := mgr.Delete(sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = s.app.SessionStore().Delete(sessionID)
	delete(s.runs.sessions, sessionID)
	for requestID, run := range s.runs.requests {
		if run.SessionID == sessionID {
			delete(s.runs.requests, requestID)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// ─── GET /sessions/{id}/info ──────────────────────────────────────────────────

func (s *Server) getSessionInfo(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	if !s.app.SessionManager().Exists(sessionID) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	sess, err := s.app.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	provider, modelID := sess.ModelInfo()

	usage, err := sess.ContextUsage(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Get workspace from config
	workspace := sess.Config().Workspace
	// Clients expand "~/" in file links with this host's home, not their own machine's.
	home, _ := os.UserHomeDir()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"home":                    home,
		"access_mode":             sess.AccessMode(),
		"context_usage":           usage,
		"id":                      sess.SessionID(),
		"provider":                provider,
		"model":                   modelID,
		"workspace":               workspace,
		"allow_outside_workspace": sess.Config().AllowOutsideWorkspace,
	})
}

// ─── POST /sessions/{id}/model ────────────────────────────────────────────────

// SwitchModelRequest is the request body for switching a session's model.
type SwitchModelRequest struct {
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"` // Optional: change provider along with model
}

func (s *Server) switchModel(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	var req SwitchModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}

	sess, err := s.app.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	if err := sess.SwitchModel(r.Context(), req.Model, req.Provider); err != nil {
		writeError(w, http.StatusInternalServerError, "switch model failed: "+err.Error())
		return
	}

	provider, modelID := sess.ModelInfo()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"provider": provider,
		"model":    modelID,
	})
}

type SetDefaultModelRequest struct {
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
}

func (s *Server) setDefaultModel(w http.ResponseWriter, r *http.Request) {
	var req SetDefaultModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}

	if err := s.app.SetDefaultModel(req.Provider, req.Model); err != nil {
		writeError(w, http.StatusInternalServerError, "set default model failed: "+err.Error())
		return
	}

	cfg := s.app.Config()
	provider := cfg.Provider
	modelID := ""
	switch provider {
	case "openai":
		modelID = cfg.OpenAIModel
	case "anthropic":
		modelID = cfg.AnthropicModel
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"provider": provider,
		"model":    modelID,
		"current":  &ModelInfo{ID: modelID, Provider: provider, Name: modelID},
	})
}

// ─── POST /sessions/{id}/compact ──────────────────────────────────────────────

// CompactRequest is the request body for context compaction.
type CompactRequest struct {
	CustomInstructions string `json:"custom_instructions,omitempty"`
}

// CompactResponse is the response for context compaction.
type CompactResponse struct {
	Summary     string `json:"summary"`
	TrimmedFrom int    `json:"trimmed_from"`
	TrimmedTo   int    `json:"trimmed_to"`
}

func (s *Server) compactSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if s.rejectActiveRun(w, sessionID) {
		return
	}

	var req CompactRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid compaction request")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	sess, err := s.app.LoadSession(ctx, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	summary, trimmedFrom, trimmedTo, err := sess.Compact(ctx, req.CustomInstructions)
	if err != nil {
		if errors.Is(err, agent.ErrAgentBusy) {
			writeError(w, http.StatusConflict, "会话仍在执行任务，请先取消或等待完成")
			return
		}
		writeError(w, http.StatusInternalServerError, "compact failed: "+err.Error())
		return
	}
	s.invalidateRunSnapshot(sessionID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(CompactResponse{
		Summary:     summary,
		TrimmedFrom: trimmedFrom,
		TrimmedTo:   trimmedTo,
	})
}

// ─── POST /sessions/{id}/command ──────────────────────────────────────────────

// CommandRequest is the request body for executing a slash command.
type CommandRequest struct {
	Command string `json:"command"` // e.g. "/model claude-sonnet-4-6"
}

// CommandResponse is the response for slash command execution.
type CommandResponse struct {
	Output      string `json:"output"`
	ShouldQuery bool   `json:"should_query"`
	QueryPrompt string `json:"query_prompt,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

func (s *Server) executeCommand(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if s.rejectActiveRun(w, sessionID) {
		return
	}

	var req CommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Command == "" {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}

	if s.slashCmds == nil {
		writeError(w, http.StatusNotImplemented, "slash commands not available in server mode")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	sess, err := s.app.LoadSession(ctx, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	cmdCtx := slashcmd.Context{
		Ctx:     ctx,
		Session: sess,
		App:     s.app,
	}

	result, err := s.slashCmds.Execute(cmdCtx, req.Command)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := CommandResponse{
		Output: result.Output,
	}
	s.invalidateRunSnapshot(sessionID)
	if result.SessionSwitchTo != nil {
		resp.SessionID = result.SessionSwitchTo.SessionID()
	}
	if result.ShouldQuery {
		resp.ShouldQuery = true
		resp.QueryPrompt = result.QueryPrompt
		if resp.QueryPrompt == "" {
			resp.QueryPrompt = "Start working..."
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// setSessionPermissions does not resolve an already pending approval or change a busy run.
func (s *Server) setSessionPermissions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Mode string `json:"mode"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || (req.Mode != "ask" && req.Mode != "full") {
		writeError(w, http.StatusBadRequest, "mode must be ask or full")
		return
	}
	s.runs.mu.Lock()
	defer s.runs.mu.Unlock()
	if state := s.runs.sessions[id]; sessionBusy(state) {
		writeError(w, http.StatusConflict, "请等待当前任务完成后切换权限")
		return
	}
	sess, err := s.app.LoadSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err := sess.TrySetAccessMode(s.confirmRunTool, req.Mode == "ask"); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"access_mode": sess.AccessMode()})
}
