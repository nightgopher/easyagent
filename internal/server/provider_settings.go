package server

import (
	"encoding/json"
	"net/http"

	"github.com/nightgopher/easyagent/sdk/config"
)

// 模型连接设置（网页端「模型连接」卡片）：读取/更新 provider、接口地址、
// API 密钥与默认模型。密钥不完整回显，仅返回脱敏提示。

type providerSettingsResponse struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	BaseURL      string `json:"base_url"`
	APIKeySet    bool   `json:"api_key_set"`
	APIKeyHint   string `json:"api_key_hint,omitempty"`
}

func providerSettingsFromConfig(cfg config.Config) providerSettingsResponse {
	resp := providerSettingsResponse{Provider: cfg.Provider}
	switch cfg.Provider {
	case "anthropic":
		resp.Model = cfg.AnthropicModel
		resp.BaseURL = cfg.AnthropicBaseURL
		resp.APIKeySet = cfg.AnthropicAPIKey != ""
		resp.APIKeyHint = maskSecret(cfg.AnthropicAPIKey)
	default:
		resp.Model = cfg.OpenAIModel
		resp.BaseURL = cfg.OpenAIBaseURL
		resp.APIKeySet = cfg.OpenAIAPIKey != ""
		resp.APIKeyHint = maskSecret(cfg.OpenAIAPIKey)
	}
	return resp
}

func (s *Server) getProviderSettings(w http.ResponseWriter, r *http.Request) {
	resp := providerSettingsFromConfig(s.app.Config())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type updateProviderSettingsRequest struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
	Model    string `json:"model"`
}

func (s *Server) updateProviderSettings(w http.ResponseWriter, r *http.Request) {
	var req updateProviderSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg, err := s.app.SetProviderConnection(req.Provider, req.BaseURL, req.APIKey, req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := providerSettingsFromConfig(cfg)
	respMap := map[string]any{
		"ok":           true,
		"provider":     resp.Provider,
		"model":        resp.Model,
		"base_url":     resp.BaseURL,
		"api_key_set":  resp.APIKeySet,
		"api_key_hint": resp.APIKeyHint,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(respMap)
}

// maskSecret 脱敏密钥：仅保留首尾各 4 位，中间以 … 代替。
func maskSecret(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "…"
	}
	return key[:4] + "…" + key[len(key)-4:]
}
