package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/sdk/config"
)

func TestSharedCodexLoginStartsUnconfiguredCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("EA_HOME", "")
	t.Setenv("EA_DESKTOP_ACCOUNT_HOME", "")
	t.Setenv("EA_SHARED_ACCOUNT_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(configDir, "EasyAgent", "core-home", "codex")
	if err = os.MkdirAll(desktop, 0700); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{"accessToken":"test","accountId":"account","expiresAt":%d}`, time.Now().Add(time.Hour).UnixMilli())
	if err = os.WriteFile(filepath.Join(desktop, "credentials.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Provider = ""
	if err = applySharedAccountDefault(context.Background(), &cfg, filepath.Join(home, ".easyagent"), "chat"); err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "codex" || cfg.CodexModel == "" {
		t.Fatalf("desktop account not adopted: provider=%q model=%q", cfg.Provider, cfg.CodexModel)
	}
	cfg.Provider = "openai"
	cfg.OpenAIModel = "my-choice"
	if err = applySharedAccountDefault(context.Background(), &cfg, filepath.Join(home, ".easyagent"), "chat"); err != nil || cfg.Provider != "openai" || cfg.OpenAIModel != "my-choice" {
		t.Fatal("existing CLI preference changed", err)
	}
}
