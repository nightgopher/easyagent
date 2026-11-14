package main

import (
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/config"
)

func TestInferredDefaultMode(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no args opens chat", want: "chat"},
		{name: "approval flag opens chat", args: []string{"-y"}, want: "chat"},
		{name: "serve subcommand", args: []string{"serve"}, want: "serve"},
		{name: "run subcommand", args: []string{"run"}, want: "run"},
		{name: "short prompt flag selects one shot", args: []string{"-p", "hello"}, want: "run"},
		{name: "long prompt flag selects one shot", args: []string{"--prompt", "hello"}, want: "run"},
		{name: "prompt equals form selects one shot", args: []string{"--prompt=hello"}, want: "run"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inferredDefaultMode(tt.args); got != tt.want {
				t.Fatalf("inferredDefaultMode(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// TestBuildSlashRegistryListsUpdate：/update 注册进 chat/serve 共用的命令表，
// /help 自动列出。
func TestBuildSlashRegistryListsUpdate(t *testing.T) {
	if !strings.Contains(buildSlashRegistry(nil).Help(), "update") {
		t.Fatal("/update must be registered in the slash command registry")
	}
}

// TestRemovedSubcommandHint：chat / interactive 子命令已移除，返回提示文案；
// 其余参数（含 serve/run/无参）不受影响。
func TestRemovedSubcommandHint(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "chat subcommand hits the notice", args: []string{"chat"}, want: removedSubcommandNotice},
		{name: "interactive subcommand hits the notice", args: []string{"interactive"}, want: removedSubcommandNotice},
		{name: "chat with extra args still hits", args: []string{"chat", "-p", "hi"}, want: removedSubcommandNotice},
		{name: "serve unaffected", args: []string{"serve"}, want: ""},
		{name: "run unaffected", args: []string{"run", "-p", "hi"}, want: ""},
		{name: "bare invocation unaffected", args: nil, want: ""},
		{name: "flag arguments unaffected", args: []string{"-y"}, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := removedSubcommandHint(tt.args); got != tt.want {
				t.Fatalf("removedSubcommandHint(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// TestApplyYoloMode：-y = 完全权限模式（自动批准 + 解除路径限制）；
// 显式 --allow-outside-workspace=false 可单独收回路径放开；非 -y 不动配置。
// flag.BoolVar 的绑定发生在 applyYoloMode 之前，因此 outsideSet=true 时
// cfg.AllowOutsideWorkspace 已是命令行传入的值。
func TestApplyYoloMode(t *testing.T) {
	for _, tt := range []struct {
		name        string
		yolo        bool
		outsideSet  bool
		flagOutside bool
		wantApprove bool
		wantOutside bool
	}{
		{name: "yolo opens both", yolo: true, outsideSet: false, flagOutside: false, wantApprove: true, wantOutside: true},
		{name: "explicit outside=false carves back the path lift", yolo: true, outsideSet: true, flagOutside: false, wantApprove: true, wantOutside: false},
		{name: "explicit outside=true stays open", yolo: true, outsideSet: true, flagOutside: true, wantApprove: true, wantOutside: true},
		{name: "without yolo config untouched", yolo: false, outsideSet: false, flagOutside: false, wantApprove: false, wantOutside: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.AutoApprove, cfg.AllowOutsideWorkspace = false, false
			if tt.outsideSet {
				cfg.AllowOutsideWorkspace = tt.flagOutside
			}
			applyYoloMode(&cfg, tt.yolo, tt.outsideSet)
			if cfg.AutoApprove != tt.wantApprove || cfg.AllowOutsideWorkspace != tt.wantOutside {
				t.Fatalf("AutoApprove=%v AllowOutsideWorkspace=%v, want %v/%v",
					cfg.AutoApprove, cfg.AllowOutsideWorkspace, tt.wantApprove, tt.wantOutside)
			}
		})
	}
}
