package app

import "github.com/nightgopher/easyagent/sdk/config"

// SetBuiltinToolSetting publishes a persisted policy only after the save has
// succeeded. Existing session snapshots and active runs are not changed.
func (a *App) SetBuiltinToolSetting(id string, enabled bool) error {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg.BuiltinToolCommandLineLocked(id) {
		return config.ErrBuiltinToolCommandLine
	}
	if err := config.SaveBuiltinToolSetting(a.cfg.DataDir, id, enabled); err != nil {
		return err
	}
	a.cfg.LoadRuntimeOverrides(a.cfg.DataDir)
	return nil
}
