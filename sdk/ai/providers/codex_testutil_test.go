package providers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// jsonUnmarshal 是 codex_test 用的普通 Unmarshal 别名（保持测试文件可读）。
func jsonUnmarshal(data []byte, out any) error { return json.Unmarshal(data, out) }

// mockEncryptedReasoning 构造 thinkingSignature：与 pi 一致，存整条 Responses reasoning item。
func mockEncryptedReasoning(id, encrypted string) string {
	item := map[string]any{
		"type":              "reasoning",
		"id":                id,
		"summary":           []any{},
		"encrypted_content": encrypted,
	}
	data, _ := json.Marshal(item)
	return string(data)
}

// itemJSON 构造一个 Responses output item 的 JSON 字符串。
func itemJSON(m map[string]any) string {
	data, _ := json.Marshal(m)
	return string(data)
}

// b64 构造 base64 字段。
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

var _ = fmt.Sprintf
