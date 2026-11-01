package config

import "fmt"

func ValidAccessMode(mode string) bool { return mode == "ask" || mode == "full" }

// InitialAccessMode applies the saved default before the legacy startup flag.
func (c Config) InitialAccessMode() string {
	if ValidAccessMode(c.DefaultAccessMode) {
		return c.DefaultAccessMode
	}
	if c.AutoApprove {
		return "full"
	}
	return "ask"
}

// SaveAccessModeDefaults preserves unrelated settings and channel defaults.
func SaveAccessModeDefaults(dataDir string, mode *string, channels map[string]string) error {
	if mode != nil && !ValidAccessMode(*mode) {
		return fmt.Errorf("default access mode must be ask or full")
	}
	for _, value := range channels {
		if !ValidAccessMode(value) {
			return fmt.Errorf("channel access mode must be ask or full")
		}
	}
	return mutateRuntimeOverrides(dataDir, func(o *runtimeOverrides) {
		if mode != nil {
			o.DefaultAccessMode = mode
		}
		if o.ChannelAccessModes == nil {
			o.ChannelAccessModes = map[string]string{}
		}
		for channel, value := range channels {
			o.ChannelAccessModes[channel] = value
		}
	})
}
