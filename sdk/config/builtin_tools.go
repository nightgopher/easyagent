package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// BuiltinToolSetting describes both the selected switch and the effect of
// independent allowed/blocked-tool filters on the coding agent.
type BuiltinToolSetting struct {
	ID           string   `json:"id"`
	Enabled      bool     `json:"enabled"`
	Effective    string   `json:"effective"` // enabled, partial, disabled
	Source       string   `json:"source"`    // startup, yaml, environment, saved, command
	SourceDetail string   `json:"source_detail,omitempty"`
	RestrictedBy []string `json:"restricted_by,omitempty"` // allowed_tools, blocked_tools
	Editable     bool     `json:"editable"`
}

type builtinToolGroup struct {
	id    string
	names []string
}

var builtinToolGroups = []builtinToolGroup{
	{"file_read", []string{"read", "grep", "find", "ls"}},
	{"file_write", []string{"write", "edit", "patch", "delete_file"}},
	{"bash", []string{"bash", "bash_output", "bash_kill"}},
	{"web_search", []string{"web_search"}},
	{"web_fetch", []string{"web_fetch"}},
}

var builtinSourceKeys = map[string]map[string]string{
	"yaml":        {"bash": "enable_bash", "web_search": "enable_web_search", "web_fetch": "enable_web"},
	"environment": {"bash": "EA_ENABLE_BASH", "web_search": "EA_ENABLE_WEB_SEARCH", "web_fetch": "EA_ENABLE_WEB"},
	"command":     {"web_search": "--web-search", "web_fetch": "--web"},
}

var ErrBuiltinToolCommandLine = errors.New("tool is controlled by a command-line option")

func ValidBuiltinTool(id string) bool {
	for _, group := range builtinToolGroups {
		if group.id == id {
			return true
		}
	}
	return false
}

func (c *Config) markBuiltinStartupSource(id, source string) {
	c.builtinStartupSource = maps.Clone(c.builtinStartupSource)
	if c.builtinStartupSource == nil {
		c.builtinStartupSource = make(map[string]string)
	}
	c.builtinStartupSource[id] = source
}

// MarkBuiltinToolCommandLine preserves explicit --web / --web-search flags
// even if a running server reloads settings. Removing the flag unlocks the switch.
func (c *Config) MarkBuiltinToolCommandLine(id string) {
	if !ValidBuiltinTool(id) {
		return
	}
	c.builtinCommandLine = maps.Clone(c.builtinCommandLine)
	if c.builtinCommandLine == nil {
		c.builtinCommandLine = make(map[string]bool)
	}
	c.builtinCommandLine[id] = true
}

func (c *Config) applyBuiltinToolOverrides(overrides map[string]bool) {
	c.builtinTools = make(map[string]bool, len(overrides))
	for id, enabled := range overrides {
		if !ValidBuiltinTool(id) {
			continue
		}
		c.builtinTools[id] = enabled
		if c.builtinCommandLine[id] {
			continue
		}
		switch id {
		case "bash":
			c.EnableBash = enabled
		case "web_search":
			c.EnableWebSearch = enabled
		case "web_fetch":
			c.EnableWeb = enabled
		}
	}
}

func (c Config) builtinEnabled(id string) bool {
	switch id {
	case "bash":
		return c.EnableBash
	case "web_search":
		return c.EnableWebSearch
	case "web_fetch":
		return c.EnableWeb
	default:
		enabled, specified := c.builtinTools[id]
		return !specified || enabled
	}
}

// EffectiveBlockedTools adds disabled built-in file groups without changing
// the user's independent allow/block filters or mutating a session snapshot.
func (c Config) EffectiveBlockedTools() []string {
	blocked := slices.Clone(c.BlockedTools)
	for _, group := range builtinToolGroups {
		if (group.id == "file_read" || group.id == "file_write") && !c.builtinEnabled(group.id) {
			blocked = append(blocked, group.names...)
		}
	}
	return blocked
}

func (c Config) BuiltinToolSettings() []BuiltinToolSetting {
	result := make([]BuiltinToolSetting, 0, len(builtinToolGroups))
	for _, group := range builtinToolGroups {
		setting := BuiltinToolSetting{ID: group.id, Enabled: c.builtinEnabled(group.id), Effective: "disabled", Source: "startup", Editable: true}
		if source := c.builtinStartupSource[group.id]; source != "" {
			setting.Source = source
		}
		if _, ok := c.builtinTools[group.id]; ok {
			setting.Source = "saved"
		}
		if c.builtinCommandLine[group.id] {
			setting.Source, setting.Editable = "command", false
		}
		setting.SourceDetail = builtinSourceKeys[setting.Source][group.id]
		if setting.Enabled {
			available := 0
			for _, name := range group.names {
				blocked := slices.Contains(c.BlockedTools, name)
				outsideAllowlist := len(c.AllowedTools) > 0 && !slices.Contains(c.AllowedTools, name)
				if blocked && !slices.Contains(setting.RestrictedBy, "blocked_tools") {
					setting.RestrictedBy = append(setting.RestrictedBy, "blocked_tools")
				}
				if outsideAllowlist && !slices.Contains(setting.RestrictedBy, "allowed_tools") {
					setting.RestrictedBy = append(setting.RestrictedBy, "allowed_tools")
				}
				if !blocked && !outsideAllowlist {
					available++
				}
			}
			// Background helpers alone cannot start a terminal command.
			if group.id == "bash" && (slices.Contains(c.BlockedTools, "bash") || len(c.AllowedTools) > 0 && !slices.Contains(c.AllowedTools, "bash")) {
				available = 0
			}
			switch available {
			case 0:
			case len(group.names):
				setting.Effective = "enabled"
			default:
				setting.Effective = "partial"
			}
		}
		result = append(result, setting)
	}
	return result
}

func (c Config) BuiltinToolCommandLineLocked(id string) bool { return c.builtinCommandLine[id] }

// SaveBuiltinToolSetting updates one switch without changing unrelated
// overrides or the allow/block lists configured by the user.
func SaveBuiltinToolSetting(dataDir, id string, enabled bool) error {
	if !ValidBuiltinTool(id) {
		return fmt.Errorf("unknown built-in tool group %q", id)
	}
	return mutateRuntimeOverrides(dataDir, func(o *runtimeOverrides) {
		if o.BuiltinTools == nil {
			o.BuiltinTools = make(map[string]bool)
		}
		o.BuiltinTools[id] = enabled
	})
}
