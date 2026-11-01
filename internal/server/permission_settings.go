package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/runtime"
)

func channelAccessMode(cfg config.Config, channel string) string {
	if channel != "" {
		if mode := cfg.ChannelAccessModes[channel]; config.ValidAccessMode(mode) {
			return mode
		}
		if channel == "feishu" {
			return "full"
		}
	}
	return cfg.InitialAccessMode()
}

func (s *Server) getPermissionSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Config()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"default_mode": cfg.InitialAccessMode(), "feishu_mode": channelAccessMode(cfg, "feishu"),
		"effective_from": "new_session",
	})
}

func (s *Server) setPermissionSettings(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DefaultMode *string `json:"default_mode"`
		FeishuMode  *string `json:"feishu_mode"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF ||
		(request.DefaultMode == nil && request.FeishuMode == nil) ||
		(request.DefaultMode != nil && !config.ValidAccessMode(*request.DefaultMode)) ||
		(request.FeishuMode != nil && !config.ValidAccessMode(*request.FeishuMode)) {
		writeError(w, http.StatusBadRequest, "permission defaults must be ask or full")
		return
	}
	channels := map[string]string{}
	if request.FeishuMode != nil {
		channels["feishu"] = *request.FeishuMode
	}
	if err := config.SaveAccessModeDefaults(s.app.Config().DataDir, request.DefaultMode, channels); err != nil {
		writeError(w, http.StatusInternalServerError, "cannot save permission defaults")
		return
	}
	s.app.ReloadRuntimeOverrides()
	s.getPermissionSettings(w, r)
}

func (s *Server) resolveChannelSession(ctx context.Context, id, channel string) (*runtime.AgentSession, error) {
	if id != "" || channel == "" {
		return s.resolveSession(ctx, id)
	}
	cfg := s.app.Config()
	cfg.DefaultAccessMode = channelAccessMode(cfg, channel)
	sess, err := s.app.SessionStore().Create(ctx, runtime.AgentSessionOptions{Config: cfg}, s.app.SessionDepsWithApp("coding"))
	if err == nil {
		err = s.app.SessionManager().SaveMeta(sess.SessionID(), sess.Workspace(), "coding")
		if err != nil {
			_ = s.app.SessionStore().Delete(sess.SessionID())
		}
	}
	return sess, err
}
