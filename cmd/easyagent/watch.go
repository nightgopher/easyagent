package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nightgopher/easyagent/internal/larkwatch"
	"github.com/nightgopher/easyagent/sdk/config"
)

const watchUsage = `usage: easyagent watch serve [--core-url URL] [--session ID] [--interval DURATION]
                              [--state-file PATH] [--exclude-chats IDS] [--max-per-wake N]
       easyagent watch once   [同上 flags，只巡查一轮]

lark 看门狗：用 lark-cli 用户身份巡查主人单聊的新消息，发现新消息才唤醒
EasyAgent 会话（忙时转插话），并把处理结果以机器人身份转达给主人。
配置也可用环境变量：EA_LARKWATCH_CORE_URL（或 PI_AGENT_URL）、EA_SERVER_API_KEY、
EA_LARKWATCH_SESSION_ID、EA_LARKWATCH_INTERVAL、EA_LARKWATCH_STATE_FILE、
EA_LARKWATCH_EXCLUDE_CHATS、EA_LARKWATCH_MAX_PER_WAKE、EA_LARKWATCH_LARK_CLI。
详见 docs/LARK_WATCH.md。`

// watchOptions 汇总 CLI flag 与环境变量的最终取值；零值表示未指定，交给默认值。
type watchOptions struct {
	command    string
	coreURL    string
	sessionID  string
	interval   time.Duration
	stateFile  string
	excludeIDs string
	maxPerWake int
	larkBin    string
}

func parseWatchArgs(args []string) (*watchOptions, error) {
	opts := &watchOptions{command: "serve"}
	if len(args) == 0 {
		return nil, errors.New(watchUsage)
	}
	switch args[0] {
	case "serve":
	case "once":
		opts.command = "once"
	default:
		return nil, fmt.Errorf("unknown watch command %q\n%s", args[0], watchUsage)
	}
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.StringVar(&opts.coreURL, "core-url", "", "EasyAgent 核心 HTTP 地址（默认取 EA_LARKWATCH_CORE_URL / PI_AGENT_URL）")
	fs.StringVar(&opts.sessionID, "session", "", "要唤醒的会话 ID（默认取 EA_LARKWATCH_SESSION_ID）")
	fs.DurationVar(&opts.interval, "interval", 0, "轮询间隔，≥10s（默认 30s）")
	fs.StringVar(&opts.stateFile, "state-file", "", "位点状态文件路径")
	fs.StringVar(&opts.excludeIDs, "exclude-chats", "", "不巡查的 chat_id，逗号分隔")
	fs.IntVar(&opts.maxPerWake, "max-per-wake", 0, "单次唤醒携带的消息上限（默认 5）")
	fs.StringVar(&opts.larkBin, "lark-cli", "", "lark-cli 可执行文件路径")
	if err := fs.Parse(args[1:]); err != nil {
		return nil, fmt.Errorf("%w\n%s", err, watchUsage)
	}
	return opts, nil
}

// runWatchCLI 是 `easyagent watch` 子命令入口；不返回（进程退出码 0 或 1）。
func runWatchCLI(args []string) {
	opts, err := parseWatchArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	cfg, err := buildWatchConfig(opts, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	injector := &larkwatch.Injector{BaseURL: cfg.CoreURL, APIKey: cfg.APIKey}
	runner := larkwatch.ExecRunner{Bin: cfg.LarkBin}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	watch, err := larkwatch.New(cfg, runner, injector, cfg.StateFile, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	ctx := context.Background()
	if opts.command == "serve" {
		var cancel context.CancelFunc
		ctx, cancel = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
	}
	if err := watch.Run(ctx); err != nil {
		logger.Error("lark watch exited with error", "error", err)
		os.Exit(1)
	}
}

// buildWatchConfig 把 CLI flag 与环境变量默认值收敛为最终配置。
// getter 抽出来便于测试。
func buildWatchConfig(opts *watchOptions, getter func(string) string) (larkwatch.Config, error) {
	cfg := larkwatch.Config{
		CoreURL:      firstNonEmpty(opts.coreURL, getter("EA_LARKWATCH_CORE_URL"), getter("PI_AGENT_URL")),
		SessionID:    firstNonEmpty(opts.sessionID, getter("EA_LARKWATCH_SESSION_ID")),
		Interval:     opts.interval,
		StateFile:    firstNonEmpty(opts.stateFile, getter("EA_LARKWATCH_STATE_FILE")),
		MaxPerWake:   opts.maxPerWake,
		LarkBin:      firstNonEmpty(opts.larkBin, getter("EA_LARKWATCH_LARK_CLI")),
		ExcludeChats: parseChatList(firstNonEmpty(opts.excludeIDs, getter("EA_LARKWATCH_EXCLUDE_CHATS"))),
		APIKey:       firstNonEmpty(getter("EA_SERVER_API_KEY"), getter("EA_API_KEY")),
		OneShot:      opts.command == "once",
	}
	if v := getter("EA_LARKWATCH_INTERVAL"); v != "" && opts.interval == 0 {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Interval = d
		}
	}
	if cfg.Interval == 0 {
		cfg.Interval = larkwatch.DefaultInterval
	}
	if v := getter("EA_LARKWATCH_RUN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.RunTimeout = d
		}
	}
	if cfg.MaxPerWake == 0 {
		if v := getter("EA_LARKWATCH_MAX_PER_WAKE"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				cfg.MaxPerWake = n
			}
		}
	}
	if cfg.SessionID == "" {
		// 未显式指定会话时，从桥接的 feishu-routes.json 自动发现
		// （单 owner 部署即开箱即用；多会话时报错并列出候选，不替人猜）。
		var candidates []string
		if eaHome := getter("EA_HOME"); eaHome != "" {
			candidates = append(candidates, filepath.Join(eaHome, "feishu-routes.json"))
		}
		candidates = append(candidates, filepath.Join(config.HomeDir(), "feishu-routes.json"))
		id, err := larkwatch.DiscoverSessionID(candidates...)
		if err != nil {
			return cfg, err
		}
		cfg.SessionID = id
	}
	if cfg.StateFile == "" {
		if base, err := os.UserConfigDir(); err == nil {
			cfg.StateFile = filepath.Join(base, "EasyAgent", "larkwatch-state.json")
		}
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func parseChatList(raw string) map[string]bool {
	out := map[string]bool{}
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
