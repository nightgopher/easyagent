package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nightgopher/easyagent/internal/agents/coding"
	"github.com/nightgopher/easyagent/internal/agents/coding/commands"
	"github.com/nightgopher/easyagent/internal/app"
	"github.com/nightgopher/easyagent/internal/command"
	"github.com/nightgopher/easyagent/internal/mode"
	userprofile "github.com/nightgopher/easyagent/internal/profile"
	"github.com/nightgopher/easyagent/internal/scheduler"
	"github.com/nightgopher/easyagent/internal/tui"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/slashcmd"
)

// version is the build version, injected via -ldflags during release builds.
var version = "dev"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Printf("easyagent %s\n", version)
		return
	}
	// Desktop's machine helper is deliberately isolated from project dotenv
	// files; its caller explicitly supplies the account home and service URL.
	if len(os.Args) >= 3 && os.Args[1] == "easycode" && slices.Contains(os.Args[2:], "--machine") {
		runEasyCodeCLI(os.Args[2:])
		return
	}
	cfg := config.Default()
	// A provider explicitly supplied by the invoking shell wins over the saved
	// CLI selection. Values loaded from .env remain the fallback configuration.
	explicitProvider := strings.TrimSpace(os.Getenv("EA_PROVIDER"))
	explicitModel := strings.TrimSpace(os.Getenv("EA_MODEL"))
	if explicitModel == "" {
		explicitModel = strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	}

	// Version flag (injectable via -ldflags "-X main.version=...")
	versionFlag := flag.Bool("version", false, "Print version and exit")

	// Config file flag (YAML, loaded before .env and env vars)
	configFile := flag.String("config", "", "Path to YAML config file (e.g. easyagent.yaml)")

	// Load .env files (ignore if missing). EA_ENV_FILE allows a custom path;
	// otherwise use the CWD and home files.
	envFile := config.Env("EA_ENV_FILE")
	if envFile != "" {
		_ = config.LoadDotEnv(envFile)
		_ = config.LoadDotEnv(envFile + ".local")
	} else {
		_ = config.LoadDotEnv(".env")
		_ = config.LoadDotEnv(".env.local")
		_ = config.LoadDotEnv(filepath.Join(config.HomeDir(), ".env"))
		_ = config.LoadDotEnv(filepath.Join(config.HomeDir(), ".env.local"))
	}
	if len(os.Args) >= 2 && os.Args[1] == "easycode" {
		runEasyCodeCLI(os.Args[2:])
		return
	}

	// Load YAML config first (lowest priority), then env vars override
	if *configFile == "" {
		candidates := []string{"easyagent.yaml", filepath.Join(config.HomeDir(), "config.yaml")}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				*configFile = candidate
				break
			}
		}
	}
	if *configFile != "" {
		if err := cfg.LoadFromYAML(*configFile); err != nil {
			slog.Warn("failed to load config file", "path", *configFile, "error", err)
		} else {
			slog.Info("loaded config file", "path", *configFile)
		}
	}

	cfg.LoadFromEnv()
	// Runtime overrides (desktop settings toggles) sit on top of env+yaml.
	cfg.LoadRuntimeOverrides(cfg.DataDir)

	// Handle --version
	if *versionFlag {
		fmt.Printf("easyagent %s\n", version)
		return
	}

	// No subcommand starts the interactive TUI. Prompt flags preserve the
	// one-shot behavior for `easyagent -p ...` and `easyagent --prompt ...`.
	args := os.Args[1:]
	modeFlag := flag.String("mode", inferredDefaultMode(args), "run, chat, interactive, or serve (default: chat; prompt flags select run)")
	listen := flag.String("listen", fmt.Sprintf("%s:%d", cfg.Host, cfg.Port), "HTTP listen address")
	input := "hello"
	flag.StringVar(&input, "prompt", input, "prompt for run mode")
	flag.StringVar(&input, "p", input, "short form of -prompt")
	sessionFlag := flag.String("session", "", "session ID (empty = new session)")
	skillDir := flag.String("skill-dir", "", "directory containing skills (SKILL.md files)")
	legacyTUI := flag.Bool("legacy", false, "Use legacy linear CLI instead of Bubble Tea TUI")
	flag.BoolVar(&cfg.AllowOutsideWorkspace, "allow-outside-workspace", cfg.AllowOutsideWorkspace, "Allow file tools outside workspace (independent of -y; OS permissions still apply)")
	flag.StringVar(&cfg.MCPConfigPath, "mcp-config", cfg.MCPConfigPath, "MCP user config path (default ~/.easyagent/mcp.json)")
	flag.StringVar(&cfg.ToolSearch, "tool-search", cfg.ToolSearch, "tool definitions: off (all) or local (on-demand)")
	flag.BoolVar(&cfg.EnableWeb, "web", cfg.EnableWeb, "Enable web page fetching (use --web=false to disable)")
	flag.BoolVar(&cfg.EnableWebSearch, "web-search", cfg.EnableWebSearch, "Enable web search (use --web-search=false to disable)")
	yolo := flag.Bool("y", false, "全权模式：初始跳过危险工具确认（会话内 /confirm on|off 随时切换）")
	if mode := modeForSubcommand(args); mode != "" {
		os.Args = append([]string{os.Args[0], "--mode", mode}, args[1:]...)
	}
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "web":
			cfg.MarkBuiltinToolCommandLine("web_fetch")
		case "web-search":
			cfg.MarkBuiltinToolCommandLine("web_search")
		}
	})
	if err := applyEasyCodeCLISelection(context.Background(), &cfg, config.HomeDir(), *modeFlag, explicitProvider, explicitModel); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if host, _, err := net.SplitHostPort(*listen); err == nil && host != "" {
		cfg.Host = host
	}

	// -y 全权模式：等价 auto_approve: true，仅决定初始状态（/confirm 可切）
	if *yolo {
		cfg.AutoApprove = true
	}

	// UI modes route diagnostics to a log file so log output cannot corrupt
	// the full-screen TUI; other modes keep stderr logging.
	closeLog, err := setupLogging(*modeFlag)
	if err != nil {
		// Report the failure before the TUI starts, then keep its terminal quiet.
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})))
		fmt.Fprintf(os.Stderr, "warning: file logging unavailable: %v\n", err)
	}
	defer closeLog()

	// Propagate version to mode package
	mode.SetVersion(version)

	// Create unified user profile (shared across agents — but each agent only
	// sees categories relevant to its domain via SummaryForCategories)
	profilePath := filepath.Join(cfg.DataDir, "user_profile.json")
	userProfile := userprofile.NewStore(profilePath)

	// Personal command center (spaces/goals/results), state under DataDir.
	commandStore := command.NewStore(cfg.DataDir)

	// Create the coding application. Knowledge browsing remains available separately.
	application, err := app.New(app.AppOptions{
		Config:      cfg,
		SkillDirs:   skillDirs(*skillDir),
		Application: coding.NewCodingApplication(cfg),
		Profile:     userProfile,
		Command:     commandStore,
	})
	if err != nil {
		slog.Error("failed to create app", "error", err)
		os.Exit(1)
	}
	defer application.Close()

	switch *modeFlag {
	case "interactive", "chat":
		sess, err := application.LoadOrCreateSession(context.Background(), *sessionFlag)
		must(err)
		cmds := buildSlashRegistry(application.LoopManager())
		if *legacyTUI {
			must(mode.NewInteractiveMode(sess, cmds, application).Run(context.Background()))
		} else {
			must(tui.Run(sess, cmds, application, sess.AccessMode() == "full"))
		}

	case "run":
		sess, err := application.LoadOrCreateSession(context.Background(), *sessionFlag)
		must(err)
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		defer cancel()
		must(mode.NewPrintMode(sess).Run(ctx, input))

	case "serve":
		// Personal command center: background discovery loop + desktop
		// notification on new inbox items. Process-lifetime goroutines.
		if cs := application.Command(); cs != nil {
			if hook := command.DesktopNotifier(); hook != nil {
				cs.SetInboxHook(hook)
			}
			command.StartAutoScan(context.Background(), cs, command.ScanIntervalFromEnv(), command.ExecGit)
		}
		cmds := buildSlashRegistry(application.LoopManager())
		srv := mode.NewServeMode(application, cmds)
		slog.Info("starting easyagent server", "listen", *listen)
		if err := srv.Run(*listen); err != nil {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", *modeFlag)
		os.Exit(2)
	}
}

func runEasyCodeCLI(args []string) {
	if err := runEasyCodeCommand(context.Background(), args, config.HomeDir(), os.Getenv("EA_EASYCODE_BASE_URL"), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func inferredDefaultMode(args []string) string {
	if mode := modeForSubcommand(args); mode != "" {
		return mode
	}
	for _, arg := range args {
		switch {
		case arg == "-p", arg == "-prompt", arg == "--prompt",
			strings.HasPrefix(arg, "-p="), strings.HasPrefix(arg, "-prompt="), strings.HasPrefix(arg, "--prompt="):
			return "run"
		}
	}
	return "chat"
}

func modeForSubcommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	switch args[0] {
	case "chat", "interactive":
		return "chat"
	case "serve", "server":
		return "serve"
	case "run":
		return "run"
	default:
		return ""
	}
}

// buildSlashRegistry creates the slash command registry with built-in commands.
func buildSlashRegistry(loopMgr *scheduler.LoopManager) *slashcmd.Registry {
	registry := slashcmd.NewRegistry()
	coding.RegisterCommands(registry)
	if loopMgr != nil {
		commands.RegisterLoopCommands(registry, loopMgr)
	}
	commands.RegisterTaskCommands(registry)
	commands.RegisterUndoCommands(registry)
	commands.RegisterFeishuCommands(registry)
	commands.RegisterWecomCommands(registry)
	return registry
}

// skillDirs returns the skill directory list from a flag value.
func skillDirs(flag string) []string {
	if flag != "" {
		return []string{flag}
	}
	return nil
}

// must panics on error.
func must(err error) {
	if err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}
