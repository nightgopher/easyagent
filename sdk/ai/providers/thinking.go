package providers

import "fmt"

// ThinkingOptions is opt-in: empty fields leave the provider/model defaults intact.
type ThinkingOptions struct {
	Mode         string // enabled, disabled, or adaptive (Anthropic)
	BudgetTokens int
	Effort       string // OpenAI-compatible reasoning_effort
}

func (o ThinkingOptions) Validate(provider string) error {
	if o.Mode != "" && o.Mode != "enabled" && o.Mode != "disabled" && !(provider == "anthropic" && o.Mode == "adaptive") {
		return fmt.Errorf("unsupported thinking mode %q", o.Mode)
	}
	switch o.Effort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("unsupported reasoning effort %q", o.Effort)
	}
	if provider == "anthropic" && o.Mode == "enabled" && o.BudgetTokens < 1024 {
		return fmt.Errorf("Anthropic thinking requires budget_tokens >= 1024")
	}
	return nil
}

type thinkingParam struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}
