package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/nightgopher/easyagent/internal/app"
	"github.com/nightgopher/easyagent/internal/easycode"
	"github.com/nightgopher/easyagent/internal/tui"
	"github.com/nightgopher/easyagent/sdk/config"
)

// CLI defaults are user-scoped and do not change the desktop host's settings.
type cliDefaultModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func cliDefaultModelPath(home string) string { return filepath.Join(home, "cli-default-model.json") }
func validCLIChoice(choice cliDefaultModel) bool {
	switch choice.Provider {
	case "openai", "anthropic", "codex":
	default:
		return false
	}
	return choice.Model != "" && len(choice.Model) <= 200 && strings.TrimSpace(choice.Model) == choice.Model && strings.IndexFunc(choice.Model, unicode.IsControl) < 0
}
func loadCLIDefault(home string) (cliDefaultModel, error) {
	var choice cliDefaultModel
	data, err := os.ReadFile(cliDefaultModelPath(home))
	if os.IsNotExist(err) {
		return choice, nil
	}
	if err != nil {
		return choice, err
	}
	if len(data) > 4096 || json.Unmarshal(data, &choice) != nil || !validCLIChoice(choice) {
		return cliDefaultModel{}, fmt.Errorf("CLI 默认模型配置无效: %s", cliDefaultModelPath(home))
	}
	return choice, nil
}
func saveCLIDefault(home string, choice cliDefaultModel) error {
	if !validCLIChoice(choice) {
		return fmt.Errorf("不支持该模型或服务商")
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(choice)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(home, ".cli-default-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), cliDefaultModelPath(home))
}
func applyCLIDefault(cfg *config.Config, home, mode, explicitProvider, explicitModel, explicitCodex, explicitAnthropic string) error {
	if mode != "chat" && mode != "interactive" && mode != "run" {
		return nil
	}
	choice, err := loadCLIDefault(home)
	if err != nil {
		return err
	}
	if choice.Provider == "" {
		return nil
	}
	if explicitProvider != "" && explicitProvider != choice.Provider {
		return nil
	}
	cfg.Provider = choice.Provider
	switch choice.Provider {
	case "openai":
		if explicitModel != "" {
			cfg.OpenAIModel = explicitModel
		} else {
			cfg.OpenAIModel = choice.Model
		}
	case "codex":
		if explicitCodex != "" {
			cfg.CodexModel = explicitCodex
		} else {
			cfg.CodexModel = choice.Model
		}
	case "anthropic":
		if explicitAnthropic != "" {
			cfg.AnthropicModel = explicitAnthropic
		} else {
			cfg.AnthropicModel = choice.Model
		}
	}
	return nil
}

type cliConfigBackend struct {
	app                                                                     *app.App
	home, explicitProvider, explicitModel, explicitCodex, explicitAnthropic string
	source                                                                  string
}

func (b *cliConfigBackend) Snapshot() tui.ConfigSnapshot {
	cfg := b.app.Config()
	model := cfg.OpenAIModel
	switch cfg.Provider {
	case "anthropic":
		model = cfg.AnthropicModel
	case "codex":
		model = cfg.CodexModel
	}
	path := cliDefaultModelPath(b.home)
	if cfg.Provider == "deepv" {
		path = filepath.Join(b.home, "easycode", "cli-selection.json")
	}
	warning := ""
	if b.explicitProvider != "" || b.explicitModel != "" || b.explicitCodex != "" || b.explicitAnthropic != "" {
		warning = "启动环境中的模型配置可能覆盖保存的默认值"
	}
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		dataDir = cfg.DataDir
	}
	source := b.source
	if source == "" {
		source = "启动配置（环境、配置文件及 CLI 默认）"
	}
	return tui.ConfigSnapshot{Provider: cfg.Provider, Model: model, Source: source, DataDir: dataDir, DefaultPath: path, Warning: warning}
}
func (b *cliConfigBackend) SetDefault(ctx context.Context, provider, model string) error {
	if provider != b.app.Config().Provider {
		return fmt.Errorf("当前只配置了 %s 服务；请先配置并重启后切换服务商", b.app.Config().Provider)
	}
	if b.explicitProvider != "" && b.explicitProvider != provider {
		return fmt.Errorf("当前 EA_PROVIDER 固定为 %s；请先移除该环境覆盖", b.explicitProvider)
	}
	if model == "" || len(model) > 200 || strings.TrimSpace(model) != model || strings.IndexFunc(model, unicode.IsControl) >= 0 {
		return fmt.Errorf("模型 ID 无效")
	}
	return b.saveDefault(ctx, provider, model)
}
func (b *cliConfigBackend) saveDefault(ctx context.Context, provider, model string) error {
	manager, err := easycode.NewManager(b.home, "")
	if err != nil {
		return err
	}
	old, err := manager.CLIModel(ctx)
	if err != nil {
		return err
	}
	if provider == "deepv" {
		if err = manager.SetCLIModel(ctx, model); err != nil {
			return err
		}
	} else {
		if old != "" {
			if err = manager.ClearCLIModel(ctx); err != nil {
				return err
			}
		}
		if err = saveCLIDefault(b.home, cliDefaultModel{Provider: provider, Model: model}); err != nil {
			if old != "" {
				if restoreErr := manager.SetCLIModel(ctx, old); restoreErr != nil {
					return fmt.Errorf("保存默认模型失败: %v；恢复 EasyCode 选择也失败: %w", err, restoreErr)
				}
			}
			return err
		}
	}
	b.app.ApplyCLIDefaultModel(provider, model)
	b.source = "本次保存的 CLI 默认"
	return nil
}
