package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/internal/stewardtools"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
)

// createSessionRecord creates a session with a resolved application/agent
// identity, mirroring the POST /sessions flow without HTTP concerns. Used by
// both the steward session endpoint and the steward dispatch tool.
func (s *Server) createSessionRecord(ctx context.Context, application, agentID, cwd, model string) (string, error) {
	cfg := s.app.Config()
	if cwd != "" {
		cfg.Workspace = cwd
	}
	if model != "" {
		switch cfg.Provider {
		case "openai", "deepv":
			cfg.OpenAIModel = model
		case "anthropic":
			cfg.AnthropicModel = model
		case "codex":
			cfg.CodexModel = model
		}
	}
	cfg, deps, err := s.app.SessionSetup(ctx, application, agentID, cfg)
	if err != nil {
		return "", err
	}
	if agentID != "" && cwd != "" && cwd != cfg.Workspace {
		return "", fmt.Errorf("use the agent workspace or edit its configuration first")
	}
	// An explicit session model overrides the agent default.
	if model != "" {
		if cfg.Provider == "anthropic" {
			cfg.AnthropicModel = model
		} else {
			cfg.OpenAIModel = model
		}
	}
	opts := runtime.AgentSessionOptions{Config: cfg}
	sess, err := s.app.SessionStore().Create(ctx, opts, deps)
	if err != nil {
		return "", err
	}
	if agentID != "" {
		err = s.app.SessionManager().SaveAgentMeta(sess.SessionID(), sess.Workspace(), agentID)
	} else {
		err = s.app.SessionManager().SaveMeta(sess.SessionID(), sess.Workspace(), application)
	}
	if err != nil {
		_ = s.app.SessionStore().Delete(sess.SessionID())
		return "", fmt.Errorf("cannot save session metadata")
	}
	return sess.SessionID(), nil
}

// dispatchStewardSession implements stewardtools.Dispatcher: spawn an
// independent task session (coding by default, or a configured business
// agent) and optionally deliver its first prompt through the session queue,
// where it starts a run as soon as the session is free.
func (s *Server) dispatchStewardSession(ctx context.Context, req stewardtools.DispatchRequest) (string, bool, error) {
	application := req.Application
	if application == "" {
		application = "coding"
	}
	if req.AgentID != "" {
		if req.AgentID == agentdata.StewardID {
			return "", false, fmt.Errorf("the steward cannot spawn another steward session")
		}
		application = "business"
	}
	sessionID, err := s.createSessionRecord(ctx, application, req.AgentID, req.Cwd, req.Model)
	if err != nil {
		return "", false, err
	}
	queued := false
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		buf := make([]byte, 8)
		_, _ = rand.Read(buf)
		mutation := queueMutation{Action: "add", ID: "steward-" + hex.EncodeToString(buf), Prompt: prompt}
		if _, qErr := s.changeQueue(sessionID, mutation); qErr == nil {
			queued = true
		}
	}
	if req.Title != "" {
		_ = s.app.SessionManager().UpdatePreferences(sessionID, sessionmgr.PreferencePatch{Title: &req.Title})
	}
	return sessionID, queued, nil
}
