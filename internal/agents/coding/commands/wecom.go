package commands

import (
	"fmt"
	"strings"

	"github.com/nightgopher/easyagent/internal/wecom"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/slashcmd"
)

// RegisterWecomCommands registers the /wecom slash command.
func RegisterWecomCommands(registry *slashcmd.Registry) {
	registry.Register(slashcmd.Command{
		Name:        "wecom",
		Description: "连接企业微信智能机器人（扫码）并管理凭据",
		Interactive: func(args string) bool {
			fields := strings.Fields(args)
			return len(fields) == 0 || fields[0] == "setup"
		},
		Subcommands: []slashcmd.Subcommand{
			{Name: "setup", Description: "扫码连接企业微信并保存机器人凭据"},
			{Name: "status", Description: "Show current connection status"},
			{Name: "logout", Description: "Clear stored credentials"},
		},
		Handler: func(ctx slashcmd.Context, args string) (slashcmd.CommandResult, error) {
			fields := strings.Fields(strings.TrimSpace(args))
			switch {
			case len(fields) == 0 || fields[0] == "setup":
				return handleWecomSetup(ctx)
			case fields[0] == "status":
				return handleWecomStatus()
			case fields[0] == "logout":
				return handleWecomLogout()
			default:
				return slashcmd.CommandResult{Output: wecomSetupUsage}, nil
			}
		},
	})
}

func handleWecomStatus() (slashcmd.CommandResult, error) {
	creds, err := wecom.LoadCredentials()
	if err != nil {
		return slashcmd.CommandResult{}, err
	}
	if creds == nil {
		return slashcmd.CommandResult{Output: "尚未连接企业微信，运行 /wecom setup 开始。"}, nil
	}
	return slashcmd.CommandResult{Output: fmt.Sprintf("已连接企业微信机器人 %s（凭据存储于 %s）。", creds.BotID, config.HomeDir())}, nil
}

func handleWecomLogout() (slashcmd.CommandResult, error) {
	if err := wecom.DeleteCredentials(); err != nil {
		return slashcmd.CommandResult{}, err
	}
	return slashcmd.CommandResult{Output: "已清除企业微信连接凭据。"}, nil
}
