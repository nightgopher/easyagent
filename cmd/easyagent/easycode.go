package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nightgopher/easyagent/internal/easycode"
	"github.com/nightgopher/easyagent/internal/feishu"
)

// The native UI starts this helper without requiring a running agent. Its JSON
// lines contain only the loopback browser URL, account name and model metadata.
func runEasyCodeCommand(ctx context.Context, args []string, home, base string, out io.Writer) error {
	machine := len(args) == 2 && args[1] == "--machine"
	if len(args) != 1 && !machine {
		return errors.New("usage: easyagent easycode login|status|models|logout")
	}
	manager, err := easycode.NewManager(home, base)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	switch args[0] {
	case "login":
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		session, err := manager.StartLogin(ctx)
		if err != nil {
			return err
		}
		defer session.Close()
		if machine {
			err = encoder.Encode(map[string]any{"type": "browser", "url": session.URL})
		} else {
			_, err = fmt.Fprintf(out, "请在浏览器中完成飞书登录：%s\n", session.URL)
			if err == nil {
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
		if machine {
			return encoder.Encode(result)
		}
		if modelErr != nil {
			_, err = fmt.Fprintln(out, "登录成功；模型目录暂不可用，请稍后重试。")
		} else {
			_, err = fmt.Fprintf(out, "登录成功，可用模型 %d 个。\n", len(models))
		}
		return err
	case "status":
		user, loggedIn, err := manager.Status(ctx)
		if err != nil {
			return err
		}
		if machine {
			return encoder.Encode(map[string]any{"type": "status", "logged_in": loggedIn, "user": user})
		}
		if loggedIn {
			_, err = fmt.Fprintln(out, "已登录 EasyCode")
		} else {
			_, err = fmt.Fprintln(out, "尚未登录 EasyCode")
		}
		return err
	case "models":
		models, err := manager.Models(ctx)
		if err != nil {
			return err
		}
		if machine {
			return encoder.Encode(map[string]any{"type": "models", "models": models})
		}
		for _, model := range models {
			if _, err = fmt.Fprintln(out, model.ID); err != nil {
				return err
			}
		}
		return nil
	case "logout":
		if err := manager.Logout(ctx); err != nil {
			return err
		}
		if machine {
			return encoder.Encode(map[string]any{"type": "logout", "ok": true})
		}
		_, err = fmt.Fprintln(out, "已退出 EasyAgent 中的 EasyCode 账号。")
		return err
	default:
		return errors.New("usage: easyagent easycode login|status|models|logout")
	}
}
