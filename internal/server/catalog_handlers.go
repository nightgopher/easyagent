package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/models"
)

// ─── GET /tools ───────────────────────────────────────────────────────────────

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	toolNames := s.app.ToolNames()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tools": toolNames,
	})
}

// ─── GET /applications ───────────────────────────────────────────────────────

func (s *Server) listApplications(w http.ResponseWriter, r *http.Request) {
	names := s.app.ApplicationNames()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"applications": names,
	})
}

// ─── GET /models ───────────────────────────────────────────────────────────────

// ModelInfo holds metadata about a single model.
type ModelInfo struct {
	SupportsImages *bool  `json:"supports_images,omitempty"`
	ID             string `json:"id"`
	Provider       string `json:"provider"`
	Name           string `json:"name"`
	// ContextWindow is set only for models whose window is known, never the
	// runtime's 128K fallback, so clients do not show a guessed size.
	ContextWindow int `json:"context_window,omitempty"`
}

func knownWindow(id string) int {
	window, _ := models.LookupContextWindow(id)
	return window
}

// ModelsResponse is the response for the models list endpoint.
type ModelsResponse struct {
	Models         []ModelInfo `json:"models"`
	Current        *ModelInfo  `json:"current,omitempty"`
	Source         string      `json:"source"`
	DiscoveryError string      `json:"discovery_error,omitempty"`
}

// gatewayModel represents a single model entry from the gateway /v1/models API.
type gatewayModel struct {
	SupportsImages  *bool    `json:"supports_images,omitempty"`
	SupportsVision  *bool    `json:"supports_vision,omitempty"`
	InputModalities []string `json:"input_modalities,omitempty"`
	ID              string   `json:"id"`
	DisplayName     string   `json:"display_name"`
}

// gatewayModelsResponse is the response shape from the gateway.
type gatewayModelsResponse struct {
	Data []gatewayModel `json:"data"`
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Config()

	// Determine current model from config
	var current *ModelInfo
	provider := cfg.Provider
	modelID := ""
	switch provider {
	case "openai":
		modelID = cfg.OpenAIModel
	case "anthropic":
		modelID = cfg.AnthropicModel
	}
	if modelID != "" {
		current = &ModelInfo{ID: modelID, Provider: provider, Name: modelID, ContextWindow: knownWindow(modelID)}
		current.SupportsImages = s.app.ResolveModel(ai.Model{ID: modelID, Provider: provider}).SupportsImages
	}

	result := ModelsResponse{Models: []ModelInfo{}, Current: current, Source: "unconfigured"}
	if provider == "openai" && cfg.OpenAIBaseURL != "" {
		models, err := s.fetchGatewayModels(r.Context(), cfg.OpenAIBaseURL, cfg.OpenAIAPIKey)
		if err != nil {
			result.DiscoveryError = err.Error()
		} else if len(models) > 0 {
			result.Models, result.Source = models, "gateway"
		}
	}
	// A configured model can still be used when discovery is unsupported. Do
	// not invent selectable models or treat this fallback as a connection test.
	if len(result.Models) == 0 && current != nil {
		result.Models, result.Source = []ModelInfo{*current}, "configured"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// fetchGatewayModels queries an OpenAI-compatible /v1/models endpoint and returns
// the list advertised by the gateway; it does not perform inference.
func (s *Server) fetchGatewayModels(ctx context.Context, baseURL, apiKey string) ([]ModelInfo, error) {
	baseURL = strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	url := baseURL + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		slog.Debug("failed to create gateway models request", "error", err)
		return nil, fmt.Errorf("model discovery URL is invalid")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("failed to fetch gateway models", "url", url, "error", err)
		return nil, fmt.Errorf("model discovery could not connect")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Debug("gateway models returned non-200", "status", resp.StatusCode)
		return nil, fmt.Errorf("model discovery returned HTTP %d", resp.StatusCode)
	}

	var gwResp gatewayModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&gwResp); err != nil {
		slog.Debug("failed to decode gateway models response", "error", err)
		return nil, fmt.Errorf("model discovery returned invalid JSON")
	}

	models := make([]ModelInfo, 0, len(gwResp.Data))
	for _, m := range gwResp.Data {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		images := m.SupportsImages
		if images == nil {
			images = ai.ImageSupport(m.SupportsVision, m.InputModalities)
		}
		resolved := s.app.ResolveModel(ai.Model{ID: m.ID, Provider: "openai", SupportsImages: images})
		models = append(models, ModelInfo{
			SupportsImages: resolved.SupportsImages,
			ID:             m.ID,
			Provider:       "openai",
			Name:           name,
			ContextWindow:  knownWindow(m.ID),
		})
	}
	return models, nil
}

// ─── POST /tools/register ────────────────────────────────────────────────────

func (s *Server) registerTool(w http.ResponseWriter, r *http.Request) {
	var def agent.ExternalToolDef
	if err := json.NewDecoder(r.Body).Decode(&def); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if def.Name == "" {
		writeError(w, http.StatusBadRequest, "tool name is required")
		return
	}
	if def.CallbackURL == "" {
		writeError(w, http.StatusBadRequest, "callback_url is required")
		return
	}

	// Validate the tool can be constructed
	if _, err := agent.NewExternalTool(def); err != nil {
		writeError(w, http.StatusBadRequest, "invalid tool: "+err.Error())
		return
	}

	s.toolMu.Lock()
	// Replace if same name exists
	updated := false
	for i, existing := range s.externalTools {
		if existing.Name == def.Name {
			s.externalTools[i] = def
			updated = true
			break
		}
	}
	if !updated {
		s.externalTools = append(s.externalTools, def)
	}
	s.syncToApp()
	s.toolMu.Unlock()

	if updated {
		slog.Info("updated external tool", "name", def.Name, "callback", def.CallbackURL)
	} else {
		slog.Info("registered external tool", "name", def.Name, "callback", def.CallbackURL)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "name": def.Name})
}

// ExternalTools returns all registered external tool definitions.
func (s *Server) ExternalTools() []agent.ExternalToolDef {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	result := make([]agent.ExternalToolDef, len(s.externalTools))
	copy(result, s.externalTools)
	return result
}

// syncToApp pushes external tools to the App (must be called with toolMu held).
func (s *Server) syncToApp() {
	defs := make([]agent.ExternalToolDef, len(s.externalTools))
	copy(defs, s.externalTools)
	s.app.SetExternalTools(defs)
}
