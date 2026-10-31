package agent

import (
	"context"
	"encoding/json"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/models"
	"github.com/nightgopher/easyagent/sdk/compaction"
)

// ContextUsage estimates the current prompt, including system and tool schemas.
// LastRequest is the latest provider measurement, not a sum across turns.
type ContextUsage struct {
	ToolDiscovery   *ToolDiscoveryUsage `json:"tool_discovery,omitempty"`
	SubagentUsage   ai.Usage            `json:"subagent_usage"` // cumulative separate-context work; excluded from EstimatedTokens
	EstimatedTokens int                 `json:"estimated_tokens"`
	ContextWindow   int                 `json:"context_window"`
	WindowKnown     bool                `json:"window_known"`
	Model           string              `json:"model"`
	Messages        int                 `json:"messages"`
	System          int                 `json:"system"`
	Tools           int                 `json:"tools"`
	LastRequest     ai.Usage            `json:"last_request"`
}

func estimateContext(req ai.StreamRequest, usage ai.Usage) ContextUsage {
	tools := 0
	if len(req.Tools) > 0 {
		data, _ := json.Marshal(req.Tools)
		tools = compaction.EstimateTextTokens(string(data))
	}
	system := 0
	if req.System != "" {
		system = compaction.EstimateTextTokens(req.System)
	}
	messages := compaction.EstimateTokens(req.Messages)
	_, known := models.LookupContextWindow(req.Model.ID)
	if req.Model.ContextWindow <= 0 {
		req.Model.ContextWindow = models.ContextWindow(req.Model.ID)
	}
	return ContextUsage{EstimatedTokens: messages + system + tools, ContextWindow: req.Model.ContextWindow, WindowKnown: known, Model: req.Model.ID, Messages: messages, System: system, Tools: tools, LastRequest: usage}
}

func (a *Agent) ContextUsage(ctx context.Context) (ContextUsage, error) {
	if a.initErr != nil {
		return ContextUsage{}, a.initErr
	}
	var history []ai.Message
	var err error
	if a.session != nil {
		history, err = a.session.BuildContext(ctx)
	}
	if err != nil {
		return ContextUsage{}, err
	}
	return a.contextUsage(ctx, a.llmRequest(history), latestRequestUsage(history))
}

func (a *Agent) contextUsage(ctx context.Context, req ai.StreamRequest, last ai.Usage) (ContextUsage, error) {
	usage := estimateContext(req, last)
	usage.ToolDiscovery = a.discoveryUsage(req, usage.Tools)
	if a.session != nil {
		var err error
		usage.SubagentUsage, err = a.session.SubagentUsage(ctx, "")
		return usage, err
	}
	for _, msg := range req.Messages {
		if tool, ok := msg.(ai.ToolResultMessage); ok && tool.SubagentUsage != nil {
			usage.SubagentUsage.Add(tool.SubagentUsage.Usage)
		}
	}
	return usage, nil
}

func (a *Agent) emitContextUsage(ctx context.Context, req ai.StreamRequest, last ai.Usage) {
	if usage, err := a.contextUsage(ctx, req, last); err == nil {
		a.emit(ctx, EventContextUsage{Usage: usage})
	}
}

func latestRequestUsage(history []ai.Message) ai.Usage {
	usage := ai.Usage{}
	for i := len(history) - 1; i >= 0; i-- {
		if msg, ok := history[i].(ai.AssistantMessage); ok {
			usage = msg.Usage
			break
		}
	}
	return usage
}
