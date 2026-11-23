package server

import (
	"github.com/nightgopher/easyagent/internal/embeddings"
	"net/http"
)

func (s *Server) knowledgeEmbeddingSettings(w http.ResponseWriter, r *http.Request) {
	store := s.app.Agents()
	if r.Method == http.MethodGet {
		config, err := store.EmbeddingConfig(r.Context())
		if err != nil {
			agentDataError(w, err)
			return
		}
		config.APIKey = ""
		writeJSON(w, config)
		return
	}
	var input struct {
		Enabled  bool   `json:"enabled"`
		Endpoint string `json:"endpoint"`
		Model    string `json:"model"`
		APIKey   string `json:"api_key"`
		ClearKey bool   `json:"clear_key"`
	}
	if !decodeAgentJSON(w, r, &input) {
		return
	}
	config, err := store.EmbeddingConfig(r.Context())
	if err != nil {
		agentDataError(w, err)
		return
	}
	config.Enabled, config.Endpoint, config.Model, config.APIKey, config.ClearKey = input.Enabled, input.Endpoint, input.Model, input.APIKey, input.ClearKey
	if err = store.SaveEmbeddingConfig(r.Context(), config); err != nil {
		agentDataError(w, err)
		return
	}
	config, err = store.EmbeddingConfig(r.Context())
	if err != nil {
		agentDataError(w, err)
		return
	}
	config.APIKey = ""
	writeJSON(w, config)
}

// knowledgeEmbeddingTest sends only a fixed sample chosen for connectivity
// verification; it never sends user knowledge to the provider.
func (s *Server) knowledgeEmbeddingTest(w http.ResponseWriter, r *http.Request) {
	config, err := s.app.Agents().EmbeddingConfig(r.Context())
	if err != nil {
		agentDataError(w, err)
		return
	}
	if !config.Enabled || config.APIKey == "" {
		writeError(w, http.StatusBadRequest, "请先启用并保存向量模型配置")
		return
	}
	client := embeddings.Client{Endpoint: config.Endpoint, Model: config.Model, APIKey: config.APIKey}
	result, err := client.Embed(r.Context(), []string{"EasyAgent embedding connection test"})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{"dimensions": len(result[0])})
}
