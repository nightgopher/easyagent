package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nightgopher/easyagent/internal/codex"
	"github.com/nightgopher/easyagent/internal/feishu"
)

// Codex 模型清单走 ChatGPT 后端 /codex/models，只有登录账号可用的模型。
var codexModels = []codex.Model{
	{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex", ContextWindow: 272000},
	{ID: "gpt-5.1-codex-max", Name: "GPT-5.1 Codex Max", ContextWindow: 272000},
	{ID: "gpt-5.1-codex", Name: "GPT-5.1 Codex", ContextWindow: 272000},
	{ID: "gpt-5.1-codex-mini", Name: "GPT-5.1 Codex Mini", ContextWindow: 272000},
}

// runCodexCommand implements `easyagent codex login|status|models|logout`.
// JSON lines keep only the loopback authorize URL, account name and model
// metadata, mirroring the easycode helper contract.
func runCodexCommand(ctx context.Context, args []string, home string, out io.Writer) error {
	machine := len(args) == 2 && args[1] == "--machine"
	if len(args) != 1 && !machine {
		return errors.New("usage: easyagent codex login|status|models|logout")
	}
	manager, err := codex.NewManager(home, "")
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	switch args[0] {
	case "login":
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		session, err := manager.StartLogin(ctx)
		if err != nil {
			return err
		}
		defer session.Close()
		if machine {
			err = encoder.Encode(map[string]any{"type": "browser", "url": session.URL})
		} else {
			_, err = fmt.Fprintf(out, "请在浏览器中完成 ChatGPT 登录：%s\n", session.URL)
			if err == nil {
				_ = feishu.OpenBrowserContext(ctx, session.URL)
			}
		}
		if err != nil {
			return err
		}
		if err := session.Wait(ctx); err != nil {
			if errors.Is(err, codex.ErrPortBusy) {
				if machine {
					_ = encoder.Encode(map[string]any{"type": "paste_required"})
					return errors.New("回调端口被占用：完成浏览器登录后，把回调地址粘贴到 `easyagent codex login-paste`")
				}
				_, err = fmt.Fprintln(out, "回调端口被占用：完成浏览器登录后，把浏览器跳转的回调地址原样粘贴到这里（回车确认）：")
				if err != nil {
					return err
				}
				var input string
				if _, err = fmt.Scanln(&input); err != nil {
					return errors.New("读取粘贴内容失败")
				}
				return session.CompleteManual(ctx, input)
			}
			return err
		}
		return printCodexLoginDone(ctx, manager, machine, out, encoder)
	case "status":
		user, loggedIn, err := manager.Status(ctx)
		if err != nil {
			return err
		}
		if machine {
			return encoder.Encode(map[string]any{"type": "status", "logged_in": loggedIn, "user": user})
		}
		if loggedIn {
			_, err = fmt.Fprintln(out, "已登录 Codex（ChatGPT 账号）")
		} else {
			_, err = fmt.Fprintln(out, "尚未登录 Codex 账号")
		}
		return err
	case "models":
		if machine {
			return encoder.Encode(map[string]any{"type": "models", "models": codexModels})
		}
		for _, model := range codexModels {
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
		_, err = fmt.Fprintln(out, "已退出 Codex（ChatGPT）账号。")
		return err
	default:
		return errors.New("usage: easyagent codex login|status|models|logout")
	}
}

func printCodexLoginDone(ctx context.Context, manager *codex.Manager, machine bool, out io.Writer, encoder *json.Encoder) error {
	cred, err := manager.CurrentCredentials(ctx)
	if err != nil {
		return err
	}
	if machine {
		return encoder.Encode(map[string]any{"type": "complete", "user": cred.User, "models": codexModels})
	}
	_, err = fmt.Fprintf(out, "登录成功，可用模型 %d 个。\n", len(codexModels))
	return err
}
