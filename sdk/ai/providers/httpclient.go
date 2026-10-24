package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nightgopher/easyagent/sdk/ai"
)

// 流式 LLM 响应的总时长可以远超常规 HTTP 请求，而 http.Client.Timeout 会把
// 整个 body 读取计入总时长，导致长生成被整体砍断。这里的替代方案：
//   - ResponseHeaderTimeout 限制首字节时间（TTFB，含上游排队）
//   - streamIdleTimeout 限制流式 body 的空闲时间：每收到一行数据重置，
//     连续无数据才中止（对齐 new-api 的 streamingTimeout 模型）
const responseHeaderTimeout = 120 * time.Second

// 变量化以便测试缩短等待。
var streamIdleTimeout = 300 * time.Second

// 空流重试（断流重发）的次数上限与基础退避间隔，实际退避 = attempt * 基础间隔。
// 变量化以便测试缩短等待。
const streamRetryAttempts = 3

var streamRetryDelay = time.Second

// 收到 200 响应头之前的瞬态失败（429/5xx、传输错误）的重试上限，与空流重试相互独立。
// 可用 EA_MODEL_MAX_RETRIES 覆盖。变量化以便测试。
var modelMaxRetries = 2

// 瞬态失败的退避：500ms 起指数退避，封顶 8s。Retry-After 超过 30s 时
// 直接报错交给用户稍后重试，不提前重发。变量化以便测试。
var (
	modelRetryBackoffBase = 500 * time.Millisecond
	modelRetryBackoffMax  = 8 * time.Second
	modelRetryAfterMax    = 30 * time.Second
)

// modelRetryBackoff 返回第 n 次（从 1 起）瞬态失败重试前的退避时间。
func modelRetryBackoff(n int) time.Duration {
	d := modelRetryBackoffBase
	for i := 1; i < n; i++ {
		d *= 2
		if d >= modelRetryBackoffMax {
			return modelRetryBackoffMax
		}
	}
	return d
}

// waitModelRetry 等待一次重试间隔，取消或超过自动等待上限时不重试。
func waitModelRetry(ctx context.Context, n int, retryAfter time.Duration) (time.Duration, bool) {
	wait := retryAfter
	if wait <= 0 {
		wait = modelRetryBackoff(n)
	}
	if wait > modelRetryAfterMax {
		return wait, false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return wait, false
	case <-timer.C:
		return wait, true
	}
}

func retryableTransportError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var network net.Error
	return (errors.As(err, &network) && network.Timeout()) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE)
}

func modelHTTPError(resp *http.Response, provider, apiKey string) *ai.HTTPStatusError {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	detail := string(body)
	if apiKey != "" {
		detail = strings.ReplaceAll(detail, apiKey, "[redacted]")
	}
	slog.Debug("model api error", "provider", provider, "status", resp.StatusCode, "body", detail)
	return ai.NewHTTPStatusError(resp.StatusCode, resp.Header.Get("Retry-After"), detail)
}

func configuredModelMaxRetries() (int, error) {
	value := strings.TrimSpace(os.Getenv("EA_MODEL_MAX_RETRIES"))
	if value == "" {
		return modelMaxRetries, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > 10 {
		return 0, fmt.Errorf("EA_MODEL_MAX_RETRIES must be an integer between 0 and 10")
	}
	return n, nil
}

func newStreamHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	return &http.Client{Transport: transport}
}

// streamWatchdog 监视流式 body 的读取进度。
type streamWatchdog struct {
	Kick    func() // 每读到一行数据调用
	Bytes   atomic.Int64
	First   atomic.Int64
	Last    atomic.Int64
	Tripped func() bool // 看门狗是否已因空闲超时触发
}

// startStreamWatchdog 启动看门狗：超过 idle 未 Kick 则 cancel（中止停滞的流）。
// ctx 需是请求所绑定的 context；cancel 是它的取消函数。
func startStreamWatchdog(ctx context.Context, idle time.Duration, cancel context.CancelFunc) *streamWatchdog {
	kickCh := make(chan struct{}, 1)
	var tripped atomic.Bool

	go func() {
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-kickCh:
				// 排掉可能已到期的 timer，避免刚 Kick 就误触发
				select {
				case <-timer.C:
				default:
				}
				timer.Reset(idle)
			case <-timer.C:
				tripped.Store(true)
				cancel()
				return
			}
		}
	}()

	return &streamWatchdog{
		Kick: func() {
			select {
			case kickCh <- struct{}{}:
			default:
			}
		},
		Tripped: tripped.Load,
	}
}

// idleTimeoutError 返回看门狗触发时用于 Result 的错误。
func idleTimeoutError(idle time.Duration) error {
	return fmt.Errorf("stream idle over %v: no data from upstream", idle)
}

func configuredStreamIdleTimeout() (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv("EA_STREAM_IDLE_TIMEOUT"))
	if value == "" {
		return streamIdleTimeout, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("EA_STREAM_IDLE_TIMEOUT must be a positive duration (for example 300s)")
	}
	return d, nil
}

type watchedReader struct {
	io.Reader
	wd *streamWatchdog
}

func (r watchedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		now := time.Now().UnixNano()
		r.wd.First.CompareAndSwap(0, now)
		r.wd.Last.Store(now)
		r.wd.Bytes.Add(int64(n))
		r.wd.Kick()
	}
	return n, err
}
func (wd *streamWatchdog) idleError(idle time.Duration, attempt int, started, headers time.Time) error {
	last := "none"
	if n := wd.Last.Load(); n > 0 {
		last = time.Since(time.Unix(0, n)).Round(time.Millisecond).String() + " ago"
	}
	first := "none"
	if n := wd.First.Load(); n > 0 {
		first = time.Unix(0, n).Sub(started).Round(time.Millisecond).String()
	}
	return fmt.Errorf("%w (request attempt %d, headers %s, first body byte %s, bytes %d, last data %s)", idleTimeoutError(idle), attempt, headers.Sub(started).Round(time.Millisecond), first, wd.Bytes.Load(), last)
}
