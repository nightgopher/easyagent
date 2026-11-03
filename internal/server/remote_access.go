package server

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nightgopher/easyagent/internal/relay"
)

func (s *Server) remoteAccess() (*relay.Host, error) {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	if s.remoteHost == nil {
		h, err := relay.NewHost(filepath.Join(s.app.Config().DataDir, "remote-access.json"), s.Handler())
		if err != nil {
			return nil, err
		}
		s.remoteHost = h
		h.Start()
	}
	return s.remoteHost, nil
}

func (s *Server) manageRemoteAccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	parsed := net.ParseIP(ip)
	if relay.DeviceID(r.Context()) != "" || s.apiKey == "" || subtle.ConstantTimeCompare([]byte(bearerToken(r)), []byte(s.apiKey)) != 1 || parsed == nil || !parsed.IsLoopback() {
		writeError(w, 403, "请通过当前电脑上已认证的 EasyAgent 客户端管理远程访问")
		return
	}
	h, err := s.remoteAccess()
	if err != nil {
		writeError(w, 503, "远程访问配置无法读取，请检查主机数据目录")
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/remote-access":
		writeDeploymentJSON(w, h.Status())
	case r.Method == "POST" && r.URL.Path == "/remote-access":
		var input struct {
			Enabled         bool   `json:"enabled"`
			RelayURL        string `json:"relay_url"`
			RegistrationKey string `json:"registration_key"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		d.DisallowUnknownFields()
		var extra any
		if d.Decode(&input) != nil || d.Decode(&extra) != io.EOF {
			writeError(w, 400, "远程访问配置格式无效")
			return
		}
		if err = h.Configure(r.Context(), input.Enabled, input.RelayURL, input.RegistrationKey); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeDeploymentJSON(w, h.Status())
	case r.Method == "POST" && r.URL.Path == "/remote-access/pair":
		name, _ := os.Hostname()
		code, err := h.NewPair(name)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeDeploymentJSON(w, code)
	case r.Method == "DELETE" && r.URL.Path == "/remote-access/pair":
		var input struct {
			PairToken string `json:"pair_token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&input); err != nil && err != io.EOF {
			writeError(w, 400, "配对取消请求格式无效")
			return
		}
		h.CancelPairIf(input.PairToken)
		writeDeploymentJSON(w, map[string]bool{"cancelled": true})
	case r.Method == "DELETE" && r.PathValue("id") != "":
		if err = h.Revoke(r.PathValue("id")); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeDeploymentJSON(w, h.Status())
	default:
		writeError(w, 405, "unsupported remote access operation")
	}
}
