package ai

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHTTPStatusError_Retryable(t *testing.T) {
	tests := []struct {
		status int
		expect bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
	}

	for _, tc := range tests {
		err := NewHTTPStatusError(tc.status, "", "")
		assert.Equal(t, tc.expect, err.Retryable(), "status %d", tc.status)
	}
}

func TestHTTPStatusError_Error(t *testing.T) {
	err := NewHTTPStatusError(401, "", `{"error":{"message":"invalid api key"}}`+"\n")
	assert.Equal(t, 401, err.StatusCode)
	assert.Equal(t, `API error 401: {"error":{"message":"invalid api key"}}`, err.Error())

	empty := NewHTTPStatusError(502, "", "")
	assert.Equal(t, "API error 502", empty.Error())
}

func TestHTTPStatusError_DetailTruncated(t *testing.T) {
	long := string(make([]byte, 600))
	err := NewHTTPStatusError(500, "", long)
	assert.Len(t, err.Detail, 200)

	// 多行正文只保留首行
	multiline := NewHTTPStatusError(500, "", "first line\nsecond line\n")
	assert.Equal(t, "first line", multiline.Detail)
}

func TestHTTPStatusError_RetryAfter(t *testing.T) {
	assert.Equal(t, 5*time.Second, NewHTTPStatusError(429, "5", "").RetryAfter)
	assert.Equal(t, time.Duration(0), NewHTTPStatusError(429, "", "").RetryAfter)
	// 已过期的 HTTP-date 不增加等待。
	assert.Equal(t, time.Duration(0), NewHTTPStatusError(429, time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat), "").RetryAfter)
	// 负数按未提供处理
	assert.Equal(t, time.Duration(0), NewHTTPStatusError(429, "-3", "").RetryAfter)
}

// 编译期保证 HTTPStatusError 实现 error 接口。
var _ error = (*HTTPStatusError)(nil)

func TestHTTPStatusError_AsErrors(t *testing.T) {
	var target *HTTPStatusError
	ok := errors.As(error(NewHTTPStatusError(429, "1", "")), &target)
	assert.True(t, ok)
	assert.Equal(t, http.StatusTooManyRequests, target.StatusCode)
}

func TestHTTPStatusErrorRetryAfterDate(t *testing.T) {
	future := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	err := NewHTTPStatusError(429, future.Format(http.TimeFormat), "busy")
	assert.InDelta(t, time.Until(future).Seconds(), err.RetryAfter.Seconds(), 1)
	assert.False(t, NewHTTPStatusError(700, "", "").Retryable())
}
