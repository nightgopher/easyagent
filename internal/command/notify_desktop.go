package command

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DesktopNotifier returns an InboxHook that pops a macOS notification for
// every new inbox item; nil on other platforms. 零依赖的「及时汇报」通道，
// 飞书/手机推送接入前先用它顶上。
func DesktopNotifier() InboxHook {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return func(item InboxItem) {
		title := "EasyAgent 指挥台"
		body := fmt.Sprintf("%s %s", item.Title, item.Body)
		body = strings.ReplaceAll(body, "\\", "")
		body = strings.ReplaceAll(body, `"`, "'")
		script := fmt.Sprintf(`display notification "%s" with title "%s" sound name "Glass"`, body, title)
		_ = exec.Command("osascript", "-e", script).Start()
	}
}

// ScanIntervalFromEnv reads EA_COMMAND_SCAN_INTERVAL (seconds). Default 60s;
// "0" disables background scanning.
func ScanIntervalFromEnv() time.Duration {
	v := strings.TrimSpace(os.Getenv("EA_COMMAND_SCAN_INTERVAL"))
	if v == "" {
		return 60 * time.Second
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 60 * time.Second
	}
	return time.Duration(n) * time.Second
}
