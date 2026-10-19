package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	authFailLimit  = 10              // 窗口内允许的错误令牌次数
	authFailWindow = time.Minute     // 统计窗口
	authBlockTime  = 5 * time.Minute // 超限后的封禁时长
	authMaxTracked = 4096            // 防止内存被伪造来源撑大
)

// authLimiter 按来源 IP 限制错误令牌尝试，防止暴力猜测。零值可用。
// 只统计“带了令牌但不正确”的请求；loopback 来源不受限，本机 UI / 桥接不会被锁。
type authLimiter struct {
	mu      sync.Mutex
	entries map[string]*authEntry
	now     func() time.Time // 测试注入；nil 用 time.Now
}

type authEntry struct {
	count        int
	windowStart  time.Time
	blockedUntil time.Time
}

func (l *authLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// remoteIP 返回请求的直连来源；不信任 X-Forwarded-For。loopback 返回空串。
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}
	return host
}

// blockedFor 返回来源剩余的封禁时长，未封禁返回 0。
func (l *authLimiter) blockedFor(ip string) time.Duration {
	if ip == "" {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[ip]; e != nil {
		if d := e.blockedUntil.Sub(l.clock()); d > 0 {
			return d
		}
	}
	return 0
}

// fail 记录一次错误令牌，返回本次是否触发封禁。
func (l *authLimiter) fail(ip string) bool {
	if ip == "" {
		return false
	}
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]*authEntry)
	}
	if len(l.entries) >= authMaxTracked {
		for k, e := range l.entries {
			if now.After(e.blockedUntil) && now.Sub(e.windowStart) > authFailWindow {
				delete(l.entries, k)
			}
		}
	}
	e := l.entries[ip]
	if e == nil {
		e = &authEntry{windowStart: now}
		l.entries[ip] = e
	}
	if now.Sub(e.windowStart) > authFailWindow {
		e.count, e.windowStart = 0, now
	}
	e.count++
	if e.count >= authFailLimit {
		e.blockedUntil = now.Add(authBlockTime)
		e.count, e.windowStart = 0, now
		return true
	}
	return false
}

// reset 在令牌校验成功后清除来源的失败计数。
func (l *authLimiter) reset(ip string) {
	if ip == "" {
		return
	}
	l.mu.Lock()
	delete(l.entries, ip)
	l.mu.Unlock()
}
