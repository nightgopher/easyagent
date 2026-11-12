package larkwatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// DiscoverSessionID 从桥接的 feishu-routes.json（owner open_id → 会话 ID 映射）
// 推断要唤醒的会话：恰好一个会话时返回它；多个会话时返回错误并列出候选，
// 不替使用者猜。paths 依次尝试，第一个可解析且含会话的文件生效。
func DiscoverSessionID(paths ...string) (string, error) {
	var lastErr error
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		raw, err := os.ReadFile(p) //nolint:gosec // 路径来自 EA_HOME/默认数据目录，非外部输入
		if err != nil {
			continue
		}
		// routes 值有两种历史形态：{"ou_x": "sess_y"} 与 {"ou_x": {"session_id": "sess_y"}}，都兼容。
		var rawRoutes map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rawRoutes); err != nil {
			lastErr = fmt.Errorf("parse %s: %w", p, err)
			continue
		}
		ids := map[string]bool{}
		for _, v := range rawRoutes {
			var s string
			if json.Unmarshal(v, &s) == nil {
				if s != "" {
					ids[s] = true
				}
				continue
			}
			var obj struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal(v, &obj) == nil && obj.SessionID != "" {
				ids[obj.SessionID] = true
			}
		}
		switch len(ids) {
		case 0:
			continue
		case 1:
			for id := range ids {
				return id, nil
			}
		default:
			list := make([]string, 0, len(ids))
			for id := range ids {
				list = append(list, id)
			}
			sort.Strings(list)
			return "", fmt.Errorf("multiple sessions in %s: %s; specify --session", p, strings.Join(list, ", "))
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("no feishu routes file found; specify --session")
}
