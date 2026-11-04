package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuiltinToolSettingsPreserveFiltersAndOtherSettings(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"unknown_future_setting":7,"default_model":"existing"}`), 0o600))
	cfg := Default()
	cfg.AllowedTools = []string{"read", "write", "web_fetch"}
	cfg.EnableBash = false
	state := func(id string) BuiltinToolSetting {
		for _, value := range cfg.BuiltinToolSettings() {
			if value.ID == id {
				return value
			}
		}
		t.Fatalf("missing group %q", id)
		return BuiltinToolSetting{}
	}
	require.Equal(t, "partial", state("file_read").Effective)
	require.Equal(t, []string{"allowed_tools"}, state("file_read").RestrictedBy)
	require.Equal(t, "disabled", state("web_search").Effective)
	require.Equal(t, "startup", state("web_fetch").Source)
	require.NoError(t, SaveBuiltinToolSetting(dir, "file_read", false))
	require.NoError(t, SaveBuiltinToolSetting(dir, "web_search", false))
	require.NoError(t, SaveBuiltinToolSetting(dir, "bash", true))
	cfg.LoadRuntimeOverrides(dir)
	require.False(t, state("file_read").Enabled)
	require.Equal(t, "saved", state("file_read").Source)
	require.Contains(t, cfg.EffectiveBlockedTools(), "read")
	require.Contains(t, cfg.EffectiveBlockedTools(), "grep")
	require.NotContains(t, cfg.BlockedTools, "read")
	require.False(t, cfg.EnableWebSearch)
	require.True(t, cfg.EnableBash)
	require.Equal(t, "disabled", state("bash").Effective, "allow-list still restricts the shell")
	cfg.AllowedTools = []string{"bash_output", "bash_kill"}
	require.Equal(t, "disabled", state("bash").Effective, "background helpers cannot start commands")
	cfg.AllowedTools = []string{"read", "write", "web_fetch"}
	require.NoError(t, SaveBuiltinToolSetting(dir, "file_read", true))
	cfg.LoadRuntimeOverrides(dir)
	require.NotContains(t, cfg.EffectiveBlockedTools(), "read")
	require.Equal(t, "partial", state("file_read").Effective)
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"unknown_future_setting": 7`)
	require.Contains(t, string(data), `"default_model": "existing"`)
	require.Error(t, SaveBuiltinToolSetting(dir, "not-a-tool", true))
}

func TestBuiltinToolSettingsReportStartupSource(t *testing.T) {
	for _, key := range []string{"EA_ENABLE_BASH", "EA_ENABLE_WEB", "EA_ENABLE_WEB_SEARCH"} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("enable_bash: false\nenable_web: true\nenable_web_search: false\n"), 0o600))
	cfg := Default()
	require.NoError(t, cfg.LoadFromYAML(path))
	lookup := func(id string) BuiltinToolSetting {
		for _, item := range cfg.BuiltinToolSettings() {
			if item.ID == id {
				return item
			}
		}
		t.Fatalf("missing %q", id)
		return BuiltinToolSetting{}
	}
	require.Equal(t, "yaml", lookup("web_search").Source)
	require.Equal(t, "enable_web_search", lookup("web_search").SourceDetail)
	require.Equal(t, "yaml", lookup("bash").Source)
	t.Setenv("EA_ENABLE_WEB_SEARCH", "true")
	cfg.LoadFromEnv()
	require.True(t, lookup("web_search").Enabled)
	require.Equal(t, "environment", lookup("web_search").Source)
	require.Equal(t, "EA_ENABLE_WEB_SEARCH", lookup("web_search").SourceDetail)
	require.NoError(t, SaveBuiltinToolSetting(dir, "web_search", false))
	cfg.LoadRuntimeOverrides(dir)
	require.Equal(t, "saved", lookup("web_search").Source)
	require.False(t, lookup("web_search").Enabled)
}

func TestBuiltinToolCommandLineRemainsAuthoritativeOnReload(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, SaveBuiltinToolSetting(dir, "web_search", false))
	cfg := Default()
	cfg.LoadRuntimeOverrides(dir)
	require.False(t, cfg.EnableWebSearch)
	cfg.EnableWebSearch = true
	cfg.MarkBuiltinToolCommandLine("web_search")
	cfg.LoadRuntimeOverrides(dir)
	require.True(t, cfg.EnableWebSearch)
	for _, setting := range cfg.BuiltinToolSettings() {
		if setting.ID == "web_search" {
			require.Equal(t, "command", setting.Source)
			require.False(t, setting.Editable)
			return
		}
	}
	t.Fatal("missing search setting")
}
