package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccessModeDefaultsOverlayAndLegacyFlags(t *testing.T) {
	cfg := Default()
	require.Equal(t, "ask", cfg.InitialAccessMode())
	cfg.AutoApprove = true
	require.Equal(t, "full", cfg.InitialAccessMode())
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"unrelated":42,"channel_access_modes":{"test":"ask"}}`), 0600))
	ask := "ask"
	require.NoError(t, SaveAccessModeDefaults(dir, &ask, map[string]string{"feishu": "full"}))
	cfg.LoadRuntimeOverrides(dir)
	require.Equal(t, "ask", cfg.InitialAccessMode())
	require.Equal(t, map[string]string{"test": "ask", "feishu": "full"}, cfg.ChannelAccessModes)
	require.True(t, cfg.AutoApprove)
	require.False(t, cfg.AllowOutsideWorkspace)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(before), `"unrelated": 42`)
	bad := "auto"
	require.Error(t, SaveAccessModeDefaults(dir, &bad, nil))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	t.Setenv("EA_DEFAULT_ACCESS_MODE", "full")
	cfg.LoadFromEnv()
	require.Equal(t, "full", cfg.InitialAccessMode())
	yaml := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(yaml, []byte("default_access_mode: ask\nchannel_access_modes:\n  feishu: ask\n"), 0600))
	require.NoError(t, cfg.LoadFromYAML(yaml))
	require.Equal(t, "ask", cfg.InitialAccessMode())
	require.Equal(t, "ask", cfg.ChannelAccessModes["feishu"])
}
