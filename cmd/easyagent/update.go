package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nightgopher/easyagent/internal/updater"
	"golang.org/x/term"
)

const updateUsage = "usage: easyagent update [--check] [-y | --yes]"

// runUpdateCLI 实现 `easyagent update` 子命令：检查并升级到最新 Release。
// 校验链与 scripts/install.sh 一致（checksums.txt SHA-256 + 暂存二进制版本回读），
// 替换是原子的，失败自动回滚保留旧安装。迷你主机 main-SHA 通道由
// easyagent-update.timer 管理，不走这里。
func runUpdateCLI(args []string) {
	checkOnly, assumeYes := false, false
	for _, arg := range args {
		switch arg {
		case "--check":
			checkOnly = true
		case "-y", "--yes":
			assumeYes = true
		case "-h", "--help":
			fmt.Println(updateUsage)
			return
		default:
			fmt.Fprintln(os.Stderr, updateUsage)
			os.Exit(2)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	u := updater.New(version)

	status, err := u.Check(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "检查更新失败:", err)
		os.Exit(1)
	}
	if !status.CurrentIsTag {
		fmt.Printf("提示: 当前版本 %s 不是 Release tag，可能由 easyagent-update 定时通道（main 构建）管理；继续将切换到 Release tag 通道。\n", status.Current)
	}
	if checkOnly || status.UpToDate {
		fmt.Printf("当前版本: %s\n最新 Release: %s\n", status.Current, status.Latest)
		return
	}

	fmt.Printf("发现新版本: %s → %s\n", status.Current, status.Latest)
	if !assumeYes && !confirm() {
		fmt.Println("已取消。")
		return
	}
	if err := u.Apply(ctx, status, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "升级失败:", err)
		os.Exit(1)
	}
}

// confirm 在交互终端询问 y/N；stdin 不是终端时拒绝并提示 -y，保证脚本场景显式确认。
func confirm() bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "非交互环境请使用 easyagent update -y 显式确认。")
		os.Exit(1)
	}
	fmt.Print("升级到最新 Release? [y/N] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}
