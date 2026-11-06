package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/nightgopher/easyagent/internal/easycode"
	"github.com/nightgopher/easyagent/internal/feishu"
	"github.com/nightgopher/easyagent/sdk/config"
)

const easyCodeUsage = "usage: easyagent easycode login [--no-browser] [--model MODEL] | status | models | use MODEL | off | logout"

type easyCodeCommandOptions struct {
	command   string
	machine   bool
	noBrowser bool
	model     string
}

func parseEasyCodeCommand(args []string) (easyCodeCommandOptions, error) {
	var opts easyCodeCommandOptions
	if len(args) == 0 {
		return opts, errors.New(easyCodeUsage)
	}
	opts.command = args[0]
	if opts.command == "use" {
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" || strings.HasPrefix(args[1], "-") {
			return opts, errors.New(easyCodeUsage)
		}
		opts.model = args[1]
		return opts, nil
	}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--machine":
			opts.machine = true
		case "--no-browser":
			opts.noBrowser = true
		case "--model":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return opts, errors.New(easyCodeUsage)
			}
			i++
			opts.model = args[i]
		default:
			return opts, errors.New(easyCodeUsage)
		}
	}
	switch opts.command {
	case "login":
		if opts.machine && (opts.noBrowser || opts.model != "") {
			return opts, errors.New(easyCodeUsage)
		}
	case "status", "models", "logout":
		if opts.noBrowser || opts.model != "" {
			return opts, errors.New(easyCodeUsage)
		}
	case "off":
		if opts.machine || opts.noBrowser || opts.model != "" {
			return opts, errors.New(easyCodeUsage)
		}
	default:
		return opts, errors.New(easyCodeUsage)
	}
	return opts, nil
}

// The native UI starts this helper without requiring a running agent. Its JSON
// lines contain only the loopback browser URL, account name and model metadata.
func runEasyCodeCommand(ctx context.Context, args []string, home, base string, out io.Writer) error {
	opts, err := parseEasyCodeCommand(args)
	if err != nil {
		return err
	}
	manager, err := easycode.NewManager(home, base)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	switch opts.command {
	case "login":
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		session, err := manager.StartLogin(ctx)
		if err != nil {
			return err
		}
		defer session.Close()
		if opts.machine {
			err = encoder.Encode(map[string]any{"type": "browser", "url": session.URL})
		} else {
			_, err = fmt.Fprintf(out, "请在浏览器中完成飞书登录：%s\n", session.URL)
			if err == nil && os.Getenv("SSH_CONNECTION") != "" {
				err = printEasyCodeSSHInstructions(out, session.URL)
			}
			if err == nil && !opts.noBrowser && os.Getenv("SSH_CONNECTION") == "" &&
				(runtime.GOOS != "linux" || os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "") {
				_ = feishu.OpenBrowserContext(ctx, session.URL)
			}
		}
		if err != nil {
			return err
		}
		user, err := session.Wait(ctx)
		if err != nil {
			return err
		}
		models, modelErr := manager.Models(ctx)
		result := map[string]any{"type": "complete", "user": user, "models": models}
		if modelErr != nil {
			result["model_error"] = modelErr.Error()
		}
		if opts.machine {
			return encoder.Encode(result)
		}
		if modelErr != nil {
			if opts.model != "" {
				return fmt.Errorf("登录成功，但暂时无法选择指定模型；请稍后运行 easyagent easycode use %s：%w", opts.model, modelErr)
			}
			_, err = fmt.Fprintln(out, "登录成功；模型目录暂不可用，CLI 模型选择未更改。请稍后运行 easyagent easycode models 和 easyagent easycode use <模型 ID>。")
			return err
		}
		if len(models) == 0 {
			if opts.model != "" {
				return fmt.Errorf("登录成功，但账号当前没有可用模型 %q", opts.model)
			}
			_, err = fmt.Fprintln(out, "登录成功，但账号当前没有可用模型，CLI 模型选择未更改。")
			return err
		}
		model, err := selectEasyCodeModel(ctx, manager, models, opts.model)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "登录成功，可用模型 %d 个；CLI 默认使用 %s（显式设置的 EA_PROVIDER 优先）。\n查看或切换：easyagent easycode models / easyagent easycode use <模型 ID>。\n", len(models), model)
		return err
	case "status":
		user, loggedIn, err := manager.Status(ctx)
		if err != nil {
			return err
		}
		if opts.machine {
			return encoder.Encode(map[string]any{"type": "status", "logged_in": loggedIn, "user": user})
		}
		if loggedIn {
			_, err = fmt.Fprintln(out, "已登录 EasyCode")
		} else {
			_, err = fmt.Fprintln(out, "尚未登录 EasyCode")
		}
		if err != nil || !loggedIn {
			return err
		}
		model, err := manager.CLIModel(ctx)
		if err != nil {
			return err
		}
		if model != "" {
			_, err = fmt.Fprintf(out, "CLI 默认模型：%s\n", model)
		}
		return err
	case "models":
		models, err := manager.Models(ctx)
		if err != nil {
			return err
		}
		if opts.machine {
			return encoder.Encode(map[string]any{"type": "models", "models": models})
		}
		for _, model := range models {
			if _, err = fmt.Fprintln(out, model.ID); err != nil {
				return err
			}
		}
		return nil
	case "use":
		models, err := manager.Models(ctx)
		if err != nil {
			return err
		}
		model, err := selectEasyCodeModel(ctx, manager, models, opts.model)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "CLI 默认使用 EasyCode 模型 %s；显式设置的 EA_PROVIDER 优先。\n", model)
		return err
	case "off":
		if err := manager.ClearCLIModel(ctx); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "已关闭 CLI 的 EasyCode 模式，恢复原有模型配置；登录状态保留。")
		return err
	case "logout":
		if err := manager.Logout(ctx); err != nil {
			return err
		}
		if opts.machine {
			return encoder.Encode(map[string]any{"type": "logout", "ok": true})
		}
		_, err = fmt.Fprintln(out, "已退出 EasyAgent 中的 EasyCode 账号。")
		return err
	default:
		return errors.New(easyCodeUsage)
	}
}

func selectEasyCodeModel(ctx context.Context, manager *easycode.Manager, models []easycode.Model, requested string) (string, error) {
	if requested == "" {
		previous, err := manager.CLIModel(ctx)
		if err != nil {
			return "", err
		}
		for _, model := range models {
			if model.ID == previous {
				requested = previous
				break
			}
		}
		if requested == "" && len(models) > 0 {
			requested = models[0].ID
		}
	}
	for _, model := range models {
		if model.ID == requested {
			return requested, manager.SetCLIModel(ctx, requested)
		}
	}
	return "", fmt.Errorf("EasyCode 账号没有可用模型 %q；请运行 easyagent easycode models 查看", requested)
}

func applyEasyCodeCLISelection(ctx context.Context, cfg *config.Config, home, mode, explicitProvider, explicitModel string) error {
	if mode != "chat" && mode != "interactive" && mode != "run" {
		return nil
	}
	if explicitProvider != "" && explicitProvider != "deepv" {
		return nil
	}
	manager, err := easycode.NewManager(home, "")
	if err != nil {
		return err
	}
	model, err := manager.CLIModel(ctx)
	if err != nil || model == "" {
		return err
	}
	cfg.Provider = "deepv"
	cfg.OpenAIModel = model
	if explicitModel != "" {
		cfg.OpenAIModel = explicitModel
	}
	return nil
}

func printEasyCodeSSHInstructions(out io.Writer, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Port() == "" {
		return errors.New("无法生成 SSH 登录转发说明")
	}
	_, err = fmt.Fprintf(out, "远程 SSH 登录：请在自己电脑的另一个终端运行 ssh -N -L %s:127.0.0.1:%s <用户>@<Linux主机>，保持连接后在该电脑浏览器打开上面的地址。\n", u.Port(), u.Port())
	return err
}
