package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSkillPreferencesPreserveOtherSettingsAndRejectCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	require.NoError(t, SetSkillEnabled(dir, path, false))
	require.NoError(t, SaveRuntimeOverrideDefaultModel(dir, "openai", "test-model"))
	disabled, err := DisabledSkillPaths(dir)
	require.NoError(t, err)
	require.True(t, disabled[path])
	require.NoError(t, SetSkillEnabled(dir, path, true))
	disabled, err = DisabledSkillPaths(dir)
	require.NoError(t, err)
	require.Empty(t, disabled)
	cfg := Default()
	cfg.LoadRuntimeOverrides(dir)
	require.Equal(t, "test-model", cfg.OpenAIModel)
	settings := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(settings, []byte("{broken"), 0600))
	require.Error(t, SetSkillEnabled(dir, path, false))
	_, err = DisabledSkillPaths(dir)
	require.Error(t, err)
	data, err := os.ReadFile(settings)
	require.NoError(t, err)
	require.Equal(t, "{broken", string(data))
}

func TestSkillPreferencesRejectSymlinkOutsideDataDirectory(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"disabled_skills":["/private/SKILL.md"]}`), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "settings.json")))
	_, err := DisabledSkillPaths(dir)
	require.Error(t, err)
}
