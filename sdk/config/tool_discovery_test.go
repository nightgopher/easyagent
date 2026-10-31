package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolSearchConfigurationAndRuntimeMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("tool_search: local\n"), 0600))
	cfg := Default()
	require.Equal(t, "off", cfg.ToolSearch)
	require.NoError(t, cfg.LoadFromYAML(path))
	require.Equal(t, "local", cfg.ToolSearch)
	t.Setenv("EA_TOOL_SEARCH", "off")
	cfg.LoadFromEnv()
	require.Equal(t, "off", cfg.ToolSearch)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"unknown_future_setting":7,"disabled_skills":["/skill"],"default_model":"existing","enable_computer_use":true}`), 0600))
	require.NoError(t, SaveRuntimeOverrideToolSearch(dir, "local"))
	cfg.LoadRuntimeOverrides(dir)
	require.Equal(t, "local", cfg.ToolSearch)
	require.True(t, cfg.EnableComputerUse)
	require.NoError(t, SaveRuntimeOverrideDefaultModel(dir, "openai", "updated"))
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	require.NoError(t, err)
	var saved map[string]any
	require.NoError(t, json.Unmarshal(data, &saved))
	require.Equal(t, "local", saved["tool_search_mode"])
	require.Equal(t, float64(7), saved["unknown_future_setting"])
	require.Equal(t, []any{"/skill"}, saved["disabled_skills"])
	for _, mode := range []string{"auto", "native", "LOCAL", ""} {
		require.Error(t, SaveRuntimeOverrideToolSearch(dir, mode))
	}
	t.Setenv("EA_TOOL_SEARCH", "invalid")
	cfg.LoadFromEnv()
	require.Error(t, ValidateToolSearch(cfg.ToolSearch))
}
