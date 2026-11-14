package commands

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nightgopher/easyagent/internal/updater"
	"github.com/nightgopher/easyagent/sdk/slashcmd"
)

// RegisterUpdateCommands registers the /update slash command, which upgrades
// the running CLI to the latest GitHub Release. The replacement is atomic;
// the current process keeps running the old binary until restart.
func RegisterUpdateCommands(registry *slashcmd.Registry, currentVersion string) {
	registry.Register(slashcmd.Command{
		Name:        "update",
		Description: "升级 easyagent 到最新 Release（重启后生效）",
		Handler: func(ctx slashcmd.Context, args string) (slashcmd.CommandResult, error) {
			if strings.TrimSpace(args) != "" {
				return slashcmd.CommandResult{}, fmt.Errorf("usage: /update")
			}

			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			u := updater.New(currentVersion)
			status, err := u.Check(cctx)
			if err != nil {
				return slashcmd.CommandResult{}, fmt.Errorf("检查更新失败: %w", err)
			}

			var out bytes.Buffer
			if !status.CurrentIsTag {
				fmt.Fprintf(&out, "提示: 当前版本 %s 不是 Release tag，可能由 easyagent-update 定时通道（main 构建）管理。\n", status.Current)
			}
			if status.UpToDate {
				fmt.Fprintf(&out, "已是最新版本（%s），无需升级。", status.Latest)
				return slashcmd.CommandResult{Output: out.String()}, nil
			}
			fmt.Fprintf(&out, "发现新版本: %s → %s\n", status.Current, status.Latest)
			if err := u.Apply(cctx, status, &out); err != nil {
				return slashcmd.CommandResult{Output: out.String()}, fmt.Errorf("升级失败: %w", err)
			}
			fmt.Fprint(&out, "请退出当前会话并重新运行 easyagent，新版本即生效。")
			return slashcmd.CommandResult{Output: out.String()}, nil
		},
	})
}
