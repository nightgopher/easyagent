package command

import (
	"context"
	"log/slog"
	"time"
)

// InboxHook is invoked for every newly appended inbox item (通知等汇报通道
// 在这里挂接；失败不影响状态写入).
type InboxHook func(item InboxItem)

// SetInboxHook registers the hook. nil clears it.
func (s *Store) SetInboxHook(h InboxHook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inboxHook = h
}

// StartAutoScan scans every space repository on a fixed interval for the
// lifetime of ctx — 定期监控状态，结果自动进收件箱。间隔 interval<=0 时返回。
// 单轮扫描限时 45s：仓库可能在慢盘/网络盘上，卡住不能拖垮循环。
func StartAutoScan(ctx context.Context, st *Store, interval time.Duration, run GitRunner) {
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				scanCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
				report := st.ScanAll(scanCtx, run)
				cancel()
				if len(report.SpaceErrs) > 0 {
					slog.Debug("command autoscan", "errs", report.SpaceErrs)
				}
			}
		}
	}()
}
