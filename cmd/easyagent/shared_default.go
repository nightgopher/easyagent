package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nightgopher/easyagent/internal/accountstore"
	"github.com/nightgopher/easyagent/internal/codex"
	"github.com/nightgopher/easyagent/internal/easycode"
	"github.com/nightgopher/easyagent/sdk/config"
)

// When CLI has no provider configured, a single existing local login can be
// used without another OAuth flow. Explicit and saved CLI settings always win.
func applySharedAccountDefault(ctx context.Context, cfg *config.Config, home, mode string) error {
	if cfg.Provider != "" || (mode != "chat" && mode != "interactive" && mode != "run") {
		return nil
	}
	cs, err := accountstore.GetStatus(home, "codex")
	if err != nil {
		return err
	}
	es, err := accountstore.GetStatus(home, "easycode")
	if err != nil {
		return err
	}
	if cs.Conflict && cs.Selected == "" {
		return fmt.Errorf("检测到两个 ChatGPT 账号；请运行 easyagent accounts use codex shared 或 desktop 选择")
	}
	if es.Conflict && es.Selected == "" {
		return fmt.Errorf("检测到两个 EasyCode 账号；请运行 easyagent accounts use easycode shared 或 desktop 选择")
	}
	if (cs.Selected == "shared" && !cs.Shared && cs.Desktop) || (cs.Selected == "desktop" && !cs.Desktop && cs.Shared) {
		return fmt.Errorf("所选 ChatGPT 账号已退出，请运行 easyagent accounts status codex 重新选择")
	}
	if (es.Selected == "shared" && !es.Shared && es.Desktop) || (es.Selected == "desktop" && !es.Desktop && es.Shared) {
		return fmt.Errorf("所选 EasyCode 账号已退出，请运行 easyagent accounts status easycode 重新选择")
	}
	var codexLogged, easyLogged bool
	if cs.Shared || cs.Desktop {
		m, e := codex.NewManager(home, "")
		if e != nil {
			return e
		}
		_, codexLogged, e = m.Status(ctx)
		if e != nil {
			return e
		}
	}
	if es.Shared || es.Desktop {
		m, e := easycode.NewManager(home, os.Getenv("EA_EASYCODE_BASE_URL"))
		if e != nil {
			return e
		}
		_, easyLogged, e = m.Status(ctx)
		if e != nil {
			return e
		}
	}
	if codexLogged && easyLogged {
		return fmt.Errorf("检测到 ChatGPT 和 EasyCode 均已登录；请运行 easyagent easycode use <模型ID>，或设置 EA_PROVIDER=codex 和 EA_CODEX_MODEL")
	}
	if codexLogged {
		cfg.Provider = "codex"
		if cfg.CodexModel == "" {
			cfg.CodexModel = codexModels[0].ID
		}
		return nil
	}
	if easyLogged {
		m, e := easycode.NewManager(home, os.Getenv("EA_EASYCODE_BASE_URL"))
		if e != nil {
			return e
		}
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		models, e := m.Models(bounded)
		if e != nil {
			return fmt.Errorf("读取 EasyCode 模型失败：%w", e)
		}
		if len(models) == 0 {
			return fmt.Errorf("EasyCode 没有可用模型")
		}
		cfg.Provider = "deepv"
		cfg.OpenAIModel = models[0].ID
	}
	return nil
}
