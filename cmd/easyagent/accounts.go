package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nightgopher/easyagent/internal/accountstore"
	"io"
)

func runAccountsCommand(args []string, home string, out io.Writer) error {
	if len(args) < 2 {
		return errors.New("用法：easyagent accounts status <codex|easycode> 或 accounts use <codex|easycode> <shared|desktop>")
	}
	switch args[0] {
	case "status":
		if len(args) != 2 && !(len(args) == 3 && args[2] == "--machine") {
			return errors.New("用法：easyagent accounts status <codex|easycode>")
		}
		status, err := accountstore.GetStatus(home, args[1])
		if err != nil {
			return err
		}
		if len(args) == 3 {
			return json.NewEncoder(out).Encode(status)
		}
		_, err = fmt.Fprintf(out, "CLI 账号：%t；桌面原账号：%t；当前使用：%s\n", status.Shared, status.Desktop, status.Selected)
		if err == nil && status.Conflict {
			_, err = fmt.Fprintln(out, "检测到两份账号，可使用 easyagent accounts use <codex|easycode> <shared|desktop> 选择；两份凭据均会保留。")
		}
		return err
	case "use":
		if len(args) != 3 {
			return errors.New("用法：easyagent accounts use <codex|easycode> <shared|desktop>")
		}
		if err := accountstore.Select(home, args[1], args[2]); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "已选择账号；下次启动生效，两份凭据均已保留。")
		return err
	default:
		return errors.New("未知账号命令")
	}
}
