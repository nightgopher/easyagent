package server

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/nightgopher/easyagent/internal/relay"
	"github.com/nightgopher/easyagent/sdk/hooks"
)

// A paired relay device is not the owner credential and must not configure
// executable commands. An unauthenticated host only permits local access.
func (s *Server) hookSettingsAllowed(r *http.Request) bool {
	if relay.DeviceID(r.Context()) != "" {
		return false
	}
	if s.apiKey != "" {
		return true
	} // auth middleware already checked the bearer token
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) getHookSettings(w http.ResponseWriter, r *http.Request) {
	if !s.hookSettingsAllowed(r) {
		writeError(w, http.StatusForbidden, "请使用主机所有者连接管理 hooks")
		return
	}
	settings, err := hooks.LoadCommandSettings(s.app.Config().HooksDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取 hooks 配置")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"hooks": settings.Hooks, "revision": hooks.CommandSettingsRevision(settings)})
}

func (s *Server) setHookSettings(w http.ResponseWriter, r *http.Request) {
	if !s.hookSettingsAllowed(r) {
		writeError(w, http.StatusForbidden, "请使用主机所有者连接管理 hooks")
		return
	}
	var req struct {
		Hooks    *[]hooks.CommandHook `json:"hooks"`
		Revision string               `json:"revision"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || req.Hooks == nil || req.Revision == "" {
		writeError(w, http.StatusBadRequest, "invalid hooks configuration")
		return
	}
	settings := hooks.CommandSettings{Hooks: *req.Hooks}
	if err := hooks.ValidateCommandSettings(settings); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := hooks.SaveCommandSettingsIfUnchanged(s.app.Config().HooksDir, req.Revision, settings); err != nil {
		if errors.Is(err, hooks.ErrCommandSettingsChanged) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "无法保存 hooks 配置")
		}
		return
	}
	s.getHookSettings(w, r)
}
