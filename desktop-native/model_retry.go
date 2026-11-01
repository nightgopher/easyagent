package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

type modelRetry struct {
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts"`
	RetryAt     int64  `json:"retry_at"`
	Reason      string `json:"reason"`
}

func (r *modelRetry) label(now time.Time) string {
	reason := "模型服务暂时不可用"
	switch r.Reason {
	case "rate_limit":
		reason = "模型服务暂时限流"
	case "network":
		reason = "模型连接暂时中断"
	}
	remaining := max(int64(0), (r.RetryAt-now.UnixMilli()+999)/1000)
	if remaining > 0 {
		return fmt.Sprintf("%s，%d 秒后自动重试（%d/%d）", reason, remaining, r.Attempt, r.MaxAttempts)
	}
	return fmt.Sprintf("正在重新请求模型（%d/%d）", r.Attempt, r.MaxAttempts)
}

func (a *preview) modelRetryView(c *ui.Context, s *session) {
	ui.Column(c.Key("model-retry/"+s.runID())).Gap(6).Padding(12).Radius(8).Background(c.Theme().Surface).Border(1, c.Theme().Border).Role(ui.RoleStatus).Children(func() {
		ui.Text(c, s.ModelRetry.label(time.Now())).FontSize(12).LineHeight(1.6)
		a.text(c, "已完成的工具操作会保留。可随时停止重试。").FontSize(12).TextColor(c.Theme().TextMuted)
	})
}
