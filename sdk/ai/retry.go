package ai

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// HTTPStatusError 表示模型 API 返回的非 200 状态码。
// 上游响应正文不整体进入错误消息（会被 ErrorMsg 带进界面与会话记录），
// 只保留截断成单行的简短摘录；provider 的调试日志同样限制正文长度。
type HTTPStatusError struct {
	StatusCode int
	// RetryAfter 来自 Retry-After 的秒数或 HTTP-date，0 表示未提供或已过期。
	RetryAfter time.Duration
	// Detail 是单行截断的正文摘录，仅作简短原因展示。
	Detail string
}

func (e *HTTPStatusError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("API error %d", e.StatusCode)
	}
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Detail)
}

// Retryable 报告该状态码是否属于瞬态故障（429 与 5xx），值得重试。
func (e *HTTPStatusError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || (e.StatusCode >= 500 && e.StatusCode <= 599)
}

// NewHTTPStatusError 从 HTTP 响应构造结构化状态码错误。
// retryAfter 是原始 Retry-After 头（秒数或 HTTP-date）。
func NewHTTPStatusError(statusCode int, retryAfter, body string) *HTTPStatusError {
	return &HTTPStatusError{
		StatusCode: statusCode,
		RetryAfter: parseRetryAfter(retryAfter),
		Detail:     truncateDetail(body),
	}
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if seconds, err := strconv.ParseInt(v, 10, 64); err == nil && seconds >= 0 {
		if seconds > int64((1<<63-1)/time.Second) {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(v); err == nil {
		if wait := time.Until(date); wait > 0 {
			return wait
		}
	}
	return 0
}

func truncateDetail(body string) string {
	body = strings.TrimSpace(body)
	if i := strings.IndexAny(body, "\r\n"); i >= 0 {
		body = body[:i]
	}
	if len(body) > 200 {
		body = body[:200]
		for !utf8.ValidString(body) {
			body = body[:len(body)-1]
		}
	}
	return body
}
