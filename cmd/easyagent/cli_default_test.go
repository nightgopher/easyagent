package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgopher/easyagent/internal/app"
	"github.com/nightgopher/easyagent/internal/easycode"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

func TestCLIDefaultIsUserScopedAndRespectsLaunchOverrides(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, saveCLIDefault(home, cliDefaultModel{Provider: "openai", Model: "my-model"}))
	for _, dir := range []string{t.TempDir(), t.TempDir()} {
		c := config.Default()
		c.DataDir = dir
		c.Provider = "openai"
		c.OpenAIModel = "old"
		require.NoError(t, applyCLIDefault(&c, home, "chat", "", "", "", ""))
		require.Equal(t, "my-model", c.OpenAIModel)
		require.NoFileExists(t, filepath.Join(dir, "settings.json"))
	}
	c := config.Default()
	c.Provider = "openai"
	c.OpenAIModel = "old"
	require.NoError(t, applyCLIDefault(&c, home, "serve", "", "", "", ""))
	require.Equal(t, "old", c.OpenAIModel)
	require.NoError(t, applyCLIDefault(&c, home, "chat", "", "one-shot", "", ""))
	require.Equal(t, "one-shot", c.OpenAIModel)
	c.Provider = "anthropic"
	require.NoError(t, applyCLIDefault(&c, home, "chat", "anthropic", "", "", ""))
	require.Equal(t, "anthropic", c.Provider)
}

func TestCLIConfigBackendPreservesEasyCodeOffBehavior(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EA_HOME", home)
	cfg := config.Default()
	cfg.Provider = "openai"
	cfg.OpenAIAPIKey = "test"
	cfg.OpenAIBaseURL = ""
	cfg.DataDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	a, err := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, err)
	defer func() { _ = a.Close() }()
	b := &cliConfigBackend{app: a, home: home}
	require.NoError(t, b.SetDefault(context.Background(), "openai", "custom-gateway-id"))
	require.Equal(t, "custom-gateway-id", b.Snapshot().Model)
	require.NoFileExists(t, filepath.Join(cfg.DataDir, "settings.json"))
	info, err := os.Stat(cliDefaultModelPath(home))
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0077)
	manager, err := easycode.NewManager(home, "")
	require.NoError(t, err)
	require.NoError(t, manager.SetCLIModel(context.Background(), "easy-model"))
	// Saving an ordinary CLI default disables the EasyCode CLI override, but retains login files.
	require.NoError(t, b.SetDefault(context.Background(), "openai", "new-model"))
	selected, err := manager.CLIModel(context.Background())
	require.NoError(t, err)
	require.Empty(t, selected)
	choice, err := loadCLIDefault(home)
	require.NoError(t, err)
	require.Equal(t, "new-model", choice.Model)
}

func TestCLIDefaultConfigRejectsMalformedWithoutOverwriting(t *testing.T) {
	home := t.TempDir()
	path := cliDefaultModelPath(home)
	bad := []byte(`{"broken":`)
	require.NoError(t, os.WriteFile(path, bad, 0600))
	_, err := loadCLIDefault(home)
	require.Error(t, err)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, bad, got)
}
