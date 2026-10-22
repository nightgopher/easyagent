package providers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelRetryBackoff：500ms 起指数退避，封顶 8s。
func TestModelRetryBackoff(t *testing.T) {
	assert.Equal(t, 500*time.Millisecond, modelRetryBackoff(1))
	assert.Equal(t, time.Second, modelRetryBackoff(2))
	assert.Equal(t, 2*time.Second, modelRetryBackoff(3))
	assert.Equal(t, 4*time.Second, modelRetryBackoff(4))
	assert.Equal(t, 8*time.Second, modelRetryBackoff(5))
	assert.Equal(t, 8*time.Second, modelRetryBackoff(50))
}

// TestWaitModelRetry：有 Retry-After 时遵守（封顶），否则指数退避；ctx 取消立即返回。
func TestWaitModelRetry(t *testing.T) {
	oldBase, oldMax, oldCap := modelRetryBackoffBase, modelRetryBackoffMax, modelRetryAfterMax
	modelRetryBackoffBase, modelRetryBackoffMax = 5*time.Millisecond, 10*time.Millisecond
	modelRetryAfterMax = 20 * time.Millisecond
	t.Cleanup(func() { modelRetryBackoffBase, modelRetryBackoffMax, modelRetryAfterMax = oldBase, oldMax, oldCap })

	// Retry-After 优先于退避
	wait, ok := waitModelRetry(context.Background(), 1, 5*time.Millisecond)
	assert.True(t, ok)
	assert.Equal(t, 5*time.Millisecond, wait)

	// Retry-After 超上限时终止自动重试，不能提前请求。
	wait, ok = waitModelRetry(context.Background(), 1, time.Hour)
	assert.False(t, ok)
	assert.Equal(t, time.Hour, wait)

	// 无 Retry-After 用退避（10ms 未触及上限）
	wait, ok = waitModelRetry(context.Background(), 2, 0)
	assert.True(t, ok)
	assert.Equal(t, 10*time.Millisecond, wait)

	// ctx 已取消：不等待，直接返回 false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wait, ok = waitModelRetry(ctx, 1, time.Hour)
	assert.False(t, ok)
	assert.Equal(t, time.Hour, wait)
}

// TestConfiguredModelMaxRetries：默认值、合法覆盖与非法值。
func TestConfiguredModelMaxRetries(t *testing.T) {
	n, err := configuredModelMaxRetries()
	require.NoError(t, err)
	assert.Equal(t, modelMaxRetries, n)

	t.Setenv("EA_MODEL_MAX_RETRIES", "5")
	n, err = configuredModelMaxRetries()
	require.NoError(t, err)
	assert.Equal(t, 5, n)

	t.Setenv("EA_MODEL_MAX_RETRIES", "0")
	n, err = configuredModelMaxRetries()
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	t.Setenv("EA_MODEL_MAX_RETRIES", "abc")
	_, err = configuredModelMaxRetries()
	assert.Error(t, err)

	t.Setenv("EA_MODEL_MAX_RETRIES", "11")
	_, err = configuredModelMaxRetries()
	assert.Error(t, err)

	t.Setenv("EA_MODEL_MAX_RETRIES", "-1")
	_, err = configuredModelMaxRetries()
	assert.Error(t, err)
}
