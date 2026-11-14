package app

import (
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/expertmarket"
)

// expertMarketState holds the shared expert-catalog client and its cached
// source. Unlike the skill market there is no tool-revision counter: installing
// a template only writes business agent/team rows, which live sessions already
// reload per turn via deps.RefreshApplication.
type expertMarketState struct {
	client *expertmarket.Client
	source *expertmarket.Source
}

// initExpertMarket builds the catalog client and cache when enabled.
func initExpertMarket(cfg config.Config) *expertMarketState {
	if !cfg.ExpertMarketEnabled {
		return nil
	}
	opts := []expertmarket.Option{}
	if cfg.ExpertMarketBaseURL != "" {
		opts = append(opts, expertmarket.WithBaseURL(cfg.ExpertMarketBaseURL))
	}
	client := expertmarket.NewClient(opts...)
	return &expertMarketState{client: client, source: expertmarket.NewSource(client)}
}

// ExpertMarket returns the shared catalog client, or nil when disabled.
func (a *App) ExpertMarket() *expertmarket.Client {
	if a.expertMarket == nil {
		return nil
	}
	return a.expertMarket.client
}

// ExpertMarketSource returns the cached catalog source, or nil when disabled.
func (a *App) ExpertMarketSource() *expertmarket.Source {
	if a.expertMarket == nil {
		return nil
	}
	return a.expertMarket.source
}
