package larkwatch

import (
	"errors"
	"strings"
	"time"
)

// Config 汇总看门狗运行参数；环境变量与命令行的解析由入口层负责，
// 字段含义见 docs/LARK_WATCH.md。
type Config struct {
	// CoreURL 是 EasyAgent 核心 HTTP 服务地址（桥接用的 PI_AGENT_URL 同源）。
	CoreURL string
	// APIKey 是核心服务 Bearer 令牌（EA_SERVER_API_KEY）。
	APIKey string
	// SessionID 是被唤醒的管家会话；必填。
	SessionID string
	// Interval 是轮询间隔，Validate 会把过小值收敛到 MinInterval。
	Interval time.Duration
	// RunTimeout 是单轮巡查（含注入的一整个 agent 任务）的超时。
	RunTimeout time.Duration
	// StateFile 保存每个会话的已处理消息位点；为空表示不落盘。
	StateFile string
	// ExcludeChats 是不巡查的 chat_id 集合（机器人会话恒排除，无需配置）。
	ExcludeChats map[string]bool
	// MaxPerWake 是单次唤醒最多带入的消息条数。
	MaxPerWake int
	// LarkBin 是 lark-cli 可执行文件路径。
	LarkBin string
	// OneShot 为 true 时只巡查一轮（systemd timer / 冒烟用）。
	OneShot bool
}

// DefaultInterval 是默认轮询间隔。
const DefaultInterval = 30 * time.Second

// MinInterval 是允许的最小轮询间隔，防止把轮询烧成常驻模型调用。
const MinInterval = 10 * time.Second

// DefaultMaxPerWake 是单次唤醒默认携带的消息上限。
const DefaultMaxPerWake = 5

// DefaultRunTimeout 是单轮巡查的默认超时。
const DefaultRunTimeout = 10 * time.Minute

// Validate 校验必填项并把过小的值收敛到下限。
func (c *Config) Validate() error {
	if strings.TrimSpace(c.SessionID) == "" {
		return errors.New("session id is required (EA_LARKWATCH_SESSION_ID or --session)")
	}
	if strings.TrimSpace(c.CoreURL) == "" {
		return errors.New("core url is required (EA_LARKWATCH_CORE_URL or PI_AGENT_URL)")
	}
	if c.Interval < MinInterval {
		c.Interval = MinInterval
	}
	if c.RunTimeout <= 0 {
		c.RunTimeout = DefaultRunTimeout
	}
	if c.MaxPerWake <= 0 {
		c.MaxPerWake = DefaultMaxPerWake
	}
	return nil
}
