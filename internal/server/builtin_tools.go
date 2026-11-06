package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nightgopher/easyagent/sdk/config"
)

func (s *Server) getBuiltinTools(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tools": s.app.Config().BuiltinToolSettings(), "effective_from": "new_session"})
}

func (s *Server) setBuiltinTools(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tool    string `json:"tool"`
		Enabled *bool  `json:"enabled"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || req.Enabled == nil || !config.ValidBuiltinTool(req.Tool) {
		writeError(w, http.StatusBadRequest, "invalid built-in tool setting")
		return
	}
	if err := s.app.SetBuiltinToolSetting(req.Tool, *req.Enabled); err != nil {
		if errors.Is(err, config.ErrBuiltinToolCommandLine) {
			writeError(w, http.StatusConflict, "tool is controlled by a startup flag")
		} else {
			writeError(w, http.StatusInternalServerError, "cannot save tool settings")
		}
		return
	}
	s.getBuiltinTools(w, r)
}
