package agent

import (
	"fmt"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/models"
)

func requestInputBudget(req ai.StreamRequest) int {
	window := req.Model.ContextWindow
	if window <= 0 {
		window = models.ContextWindow(req.Model.ID)
	}
	output := 4096
	if req.MaxTokens != nil {
		output = *req.MaxTokens
	}
	return window - output
}

// admitDiscovery runs serially in history order, after execution/hooks and before
// persistence. Parallel searches cannot each consume the same remaining budget.
func (a *Agent) admitDiscovery(history []ai.Message, result ai.ToolResultMessage) ai.ToolResultMessage {
	if a.discovery == nil || result.IsError {
		return result
	}
	record, ok := readDiscoveryRecord(result.Details)
	if !ok || len(record.Selected) == 0 {
		return result
	}
	req := a.llmRequest(append(append([]ai.Message(nil), history...), result))
	usage := estimateContext(req, ai.Usage{})
	// Reserve some history even if the current conversation is short. Full
	// history growth is handled by the shared compaction path before sending.
	headroom := min(4096, max(0, requestInputBudget(req)/8))
	if usage.System+usage.Tools+headroom > requestInputBudget(req) {
		result.IsError = true
		result.Details = nil
		result.Content = "Tool definitions exceed the available context budget. No tools were loaded by this search. Search an exact name, reduce limit, reduce always-loaded tools, or choose a model with more context."
	}
	return result
}

func (a *Agent) validateRequestBudget(req ai.StreamRequest) error {
	if a.discovery == nil || a.discovery.deferred == 0 {
		return nil
	}
	if estimateContext(req, ai.Usage{}).EstimatedTokens > requestInputBudget(req) {
		return fmt.Errorf("tool discovery: request exceeds the model context budget after compaction; shorten the conversation or reduce always-loaded tools")
	}
	return nil
}
