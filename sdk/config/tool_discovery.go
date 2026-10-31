package config

import "fmt"

func ValidateToolSearch(mode string) error {
	if mode != "" && mode != "off" && mode != "local" {
		return fmt.Errorf("invalid tool_search %q: use off or local", mode)
	}
	return nil
}

func SaveRuntimeOverrideToolSearch(dataDir, mode string) error {
	if mode == "" {
		return fmt.Errorf("tool search mode is required")
	}
	if err := ValidateToolSearch(mode); err != nil {
		return err
	}
	return mutateRuntimeOverrides(dataDir, func(o *runtimeOverrides) { o.ToolSearchMode = &mode })
}
