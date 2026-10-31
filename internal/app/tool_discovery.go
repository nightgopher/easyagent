package app

import (
	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/config"
)

type ToolDiscoverySettings struct {
	Mode          string `json:"mode"`
	Revision      uint64 `json:"revision"`
	EffectiveFrom string `json:"effective_from"`
}

func (a *App) toolDiscoveryPolicy() (agent.ToolSearchMode, uint64) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return agent.ToolSearchMode(a.cfg.ToolSearch), a.discoveryRevision
}

func (a *App) ToolDiscoverySettings() ToolDiscoverySettings {
	mode, revision := a.toolDiscoveryPolicy()
	return ToolDiscoverySettings{Mode: string(mode), Revision: revision, EffectiveFrom: "next_run"}
}

// Save first, then publish one atomic policy snapshot. Active sessions keep
// their Agent; the runtime observes the revision before the next task.
func (a *App) SetToolDiscovery(mode string) error {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if err := config.SaveRuntimeOverrideToolSearch(a.cfg.DataDir, mode); err != nil {
		return err
	}
	if a.cfg.ToolSearch != mode {
		a.cfg.ToolSearch = mode
		a.discoveryRevision++
	}
	return nil
}
