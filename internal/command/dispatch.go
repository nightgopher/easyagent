package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Dispatcher drives a real, logged-in desktop app (verification A: Codex
// desktop) through the opening moves of a goal: new chat → select model →
// paste kickoff → send. docs/decisions/personal-command-center.md「执行」。
// GUI automation covers only these few actions; state and results come back
// through git (discovery), never through the UI.
type Dispatcher struct {
	Bin   string // agent-desktop CLI path
	App   string // target app display name (macOS bundle com.openai.codex shows as "ChatGPT")
	Model string // model to select in the app, e.g. "GPT-6 Luna"
}

// NewCodexDispatcher returns a dispatcher for the Codex desktop app with the
// cheapest model preselected, or nil on non-macOS.
func NewCodexDispatcher() *Dispatcher {
	if runtime.GOOS != "darwin" {
		return nil
	}
	bin := os.Getenv("EA_AGENT_DESKTOP")
	if bin == "" {
		if p, err := exec.LookPath("agent-desktop"); err == nil {
			bin = p
		} else if home, err := os.UserHomeDir(); err == nil {
			if _, err := os.Stat(filepath.Join(home, ".local", "bin", "agent-desktop")); err == nil {
				bin = filepath.Join(home, ".local", "bin", "agent-desktop")
			}
		}
	}
	if bin == "" {
		return nil
	}
	return &Dispatcher{Bin: bin, App: "ChatGPT", Model: "GPT-6 Luna"}
}

var (
	dispatchMu sync.Mutex // 一次只有一个派发在驱动 GUI
	dialTO     = 30 * time.Second
)

// DispatchLog is one progress line returned to the caller.
type DispatchLog struct {
	Step string `json:"step"`
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

// Dispatch sends the kickoff text into a fresh conversation of the target app
// and returns the step-by-step transcript. The GUI is driven headlessly where
// the app exposes semantic actions and physically (focused window) where it
// does not — the same mix proven by verification A (5/5 consecutive runs).
func (d *Dispatcher) Dispatch(ctx context.Context, kickoff string) ([]DispatchLog, error) {
	// 首选 CDP 后台通道：零鼠标、零焦点。失败且未允许物理模式时直接报错。
	logs, cdpErr := d.DispatchCDP(ctx, kickoff)
	if cdpErr == nil {
		return logs, nil
	}
	if os.Getenv("EA_DISPATCH_ALLOW_HEADED") != "1" {
		return logs, fmt.Errorf("后台派发不可用：%v（如确需物理模式，设 EA_DISPATCH_ALLOW_HEADED=1，会短暂占用鼠标键盘）", cdpErr)
	}
	return d.dispatchHeaded(ctx, kickoff, logs, cdpErr)
}

// dispatchHeaded is the legacy physical-input flow (verification A). Only used
// when EA_DISPATCH_ALLOW_HEADED=1 because it takes over pointer and focus.
func (d *Dispatcher) dispatchHeaded(ctx context.Context, kickoff string, log0 []DispatchLog, cdpErr error) ([]DispatchLog, error) {
	dispatchMu.Lock()
	defer dispatchMu.Unlock()
	log := append(log0,
		DispatchLog{Step: "CDP 后台通道", OK: false, Note: cdpErr.Error()},
		DispatchLog{Step: "回退物理模式（EA_DISPATCH_ALLOW_HEADED=1）", OK: true})
	step := func(name string, ok bool, note string) {
		log = append(log, DispatchLog{Step: name, OK: ok, Note: note})
	}

	// 1. New chat (first match in tree order is the sidebar button).
	if ok, note := d.clickBy(ctx, "button", `^New chat$`, "", 6); ok {
		step("新建对话", true, note)
	} else {
		step("新建对话", false, note)
		return log, fmt.Errorf("找不到 New chat 按钮: %s", note)
	}
	time.Sleep(2 * time.Second)

	// 2. Model picker (combobox labelled with the current model name).
	if ok, note := d.clickBy(ctx, "combobox", `GPT-6`, "", 8); ok {
		step("打开模型选择", true, note)
	} else {
		step("打开模型选择", false, note)
		return log, fmt.Errorf("找不到模型选择器: %s", note)
	}
	time.Sleep(1500 * time.Millisecond)

	// 3. Optional two-level menu (Select model), then the model item.
	if ok, _ := d.clickBy(ctx, "menuitem", `^Select model$`, "", 3); ok {
		time.Sleep(1200 * time.Millisecond)
	}
	if ok, note := d.clickBy(ctx, "menuitem", regexp.QuoteMeta(d.Model), "", 8); ok {
		step("选择模型 "+d.Model, true, note)
	} else {
		step("选择模型 "+d.Model, false, note)
		return log, fmt.Errorf("模型菜单里没有 %s: %s", d.Model, note)
	}
	time.Sleep(1500 * time.Millisecond)

	// 4. Composer input.
	if ok, note := d.clickBy(ctx, "textfield", `^Do anything$`, "", 8); ok {
		step("定位输入框", true, note)
	} else {
		step("定位输入框", false, note)
		return log, fmt.Errorf("找不到输入框: %s", note)
	}
	time.Sleep(1 * time.Second)

	// 5. Paste kickoff via clipboard (contenteditable rejects AX set-value).
	if err := clipboardSet(kickoff); err != nil {
		step("写入剪贴板", false, err.Error())
		return log, err
	}
	if ok := d.press(ctx, "cmd+v"); ok {
		step("粘贴开工包", true, "")
	} else {
		step("粘贴开工包", false, "cmd+v 未送达")
		return log, fmt.Errorf("粘贴失败")
	}
	time.Sleep(1500 * time.Millisecond)

	// 6. Send.
	if ok, note := d.clickBy(ctx, "button", `^Send$`, "", 8); ok {
		step("发送", true, note)
	} else {
		step("发送", false, note)
		return log, fmt.Errorf("找不到发送按钮: %s", note)
	}
	return log, nil
}

// ── agent-desktop CLI plumbing ───────────────────────────────────────────

type adNode struct {
	Name     string    `json:"name"`
	Role     string    `json:"role"`
	RefID    string    `json:"ref_id"`
	Children []*adNode `json:"children"`
}

func (d *Dispatcher) ad(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dialTO)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (d *Dispatcher) snapshotTree(ctx context.Context) (*adNode, error) {
	out, err := d.ad("snapshot", "--app", d.App, "-i", "--compact", "--max-depth", "22")
	if err != nil {
		return nil, err
	}
	var env struct {
		Data struct {
			Tree *adNode `json:"tree"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return nil, err
	}
	if env.Data.Tree == nil {
		return nil, fmt.Errorf("snapshot 无树")
	}
	return env.Data.Tree, nil
}

func findNode(n *adNode, role string, nameRe *regexp.Regexp) (string, bool) {
	if n == nil {
		return "", false
	}
	if n.Role == role && nameRe.MatchString(n.Name) && n.RefID != "" {
		return n.RefID, true
	}
	for _, c := range n.Children {
		if ref, ok := findNode(c, role, nameRe); ok {
			return ref, true
		}
	}
	return "", false
}

// clickBy snapshots and physically clicks the first matching interactive
// element; retried because the freshly focused surface re-renders.
func (d *Dispatcher) clickBy(ctx context.Context, role, nameRe, exclude string, tries int) (bool, string) {
	re := regexp.MustCompile(nameRe)
	_ = exclude // 全匹配首个命中即可；预留排除规则
	for i := 0; i < tries; i++ {
		tree, err := d.snapshotTree(ctx)
		if err != nil {
			time.Sleep(1500 * time.Millisecond)
			continue
		}
		ref, ok := findNode(tree, role, re)
		if !ok {
			time.Sleep(1500 * time.Millisecond)
			continue
		}
		if _, err := d.ad("--headed", "click", ref); err != nil {
			time.Sleep(1500 * time.Millisecond)
			continue
		}
		return true, ref
	}
	return false, "重试耗尽"
}

func (d *Dispatcher) press(ctx context.Context, key string) bool {
	_, err := d.ad("press", key)
	return err == nil
}

// clipboardSet writes text to the macOS clipboard via pbcopy.
func clipboardSet(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
