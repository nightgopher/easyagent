package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// CDP 端口：Codex 桌面版（Electron）带 --remote-debugging-port 启动后暴露的
// DevTools 通道。通过它直接操作页面 DOM——全程后台，不抢鼠标、不动焦点。
const (
	cdpPortEnv  = "EA_CODEX_CDP_PORT"
	cdpAppLong  = "ChatGPT" // macOS 应用名（bundle com.openai.codex）
	cdpFlagPort = 9222
)

func cdpBase() string {
	if v := os.Getenv(cdpPortEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return fmt.Sprintf("http://127.0.0.1:%d", n)
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%d", cdpFlagPort)
}

// cdpPageWS returns the WebSocket debugger URL of the app's page target.
func cdpPageWS(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", cdpBase()+"/json/list", nil)
	var client http.Client
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var targets []struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return "", err
	}
	pick := ""
	for _, t := range targets {
		if t.Type != "page" {
			continue
		}
		if strings.Contains(t.Title, "ChatGPT") || strings.Contains(t.Title, "Codex") {
			pick = t.ID
			break
		}
		if pick == "" {
			pick = t.ID
		}
	}
	if pick == "" {
		return "", fmt.Errorf("无 page 调试目标")
	}
	return fmt.Sprintf("ws://127.0.0.1:%d/devtools/page/%s", cdpFlagPort, pick), nil
}

// cdpEvaluate runs a JS expression in the page and returns result.value.
func cdpEvaluate(ctx context.Context, wsURL, expr string) (string, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(dialCtx, wsURL, nil)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.WriteJSON(map[string]any{
		"id":     1,
		"method": "Runtime.evaluate",
		"params": map[string]any{
			"expression":   expr,
			"returnByValue": true,
			"awaitPromise": true,
		},
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return "", err
		}
		var msg struct {
			ID     int `json:"id"`
			Result struct {
				Result struct {
					Value any `json:"value"`
				} `json:"result"`
			} `json:"result"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			return "", err
		}
		if msg.ID != 1 {
			continue
		}
		switch v := msg.Result.Result.Value.(type) {
		case string:
			return v, nil
		case nil:
			return "", nil
		default:
			b, _ := json.Marshal(v)
			return string(b), nil
		}
	}
}

// codexTaskRunning 通过 AX 检查 Codex 是否有任务在跑（运行中界面有 Stop 按钮）。
func codexTaskRunning(bin, app string) bool {
	out, err := exec.Command(bin, "snapshot", "--app", app, "-i", "--compact", "--max-depth", "18").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), `"name":"Stop"`)
}

// ensureCDP returns a live page debugger URL. When the port is absent and no
// Codex task is mid-flight, the app is relaunched once with the debug port.
// Relaunching kills nothing mid-work; this is the documented trade-off.
func (d *Dispatcher) ensureCDP(ctx context.Context) (string, bool, error) {
	if ws, err := cdpPageWS(ctx); err == nil {
		return ws, false, nil
	}
	if codexTaskRunning(d.Bin, d.App) {
		return "", false, fmt.Errorf("Codex 桌面版有任务正在执行，不能带调试端口重启；等它完成后再派发")
	}
	if err := exec.Command("osascript", "-e", `tell application "`+cdpAppLong+`" to quit`).Run(); err != nil {
		return "", false, fmt.Errorf("退出 %s: %v", cdpAppLong, err)
	}
	time.Sleep(2500 * time.Millisecond)
	if err := exec.Command("open", "-a", cdpAppLong, "--args",
		"--remote-debugging-port", strconv.Itoa(cdpFlagPort)).Run(); err != nil {
		return "", false, fmt.Errorf("带调试端口启动 %s: %v", cdpAppLong, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1200 * time.Millisecond)
		if ws, err := cdpPageWS(ctx); err == nil {
			return ws, true, nil
		}
	}
	return "", false, fmt.Errorf("重启后仍未出现调试端口 %d", cdpFlagPort)
}

// jsStep wraps a JS snippet; returns ok + note parsed from the page answer.
func (d *Dispatcher) jsStep(ctx context.Context, wsURL, name, expr string) (bool, string, error) {
	out, err := cdpEvaluate(ctx, wsURL, expr)
	if err != nil {
		return false, "", err
	}
	var res struct {
		OK  bool   `json:"ok"`
		Err string `json:"err"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return false, out, nil // 非 JSON 返回也当作结果备注
	}
	if !res.OK {
		return false, res.Err, nil
	}
	return true, "", nil
}

const (
	jsNewChat = `(() => {
  const cand = [...document.querySelectorAll('button,[role="button"]')]
    .filter(e => /new chat/i.test((e.getAttribute('aria-label')||'') + ' ' + (e.textContent||'')));
  if (!cand.length) return JSON.stringify({ok:false, err:'找不到 New chat'});
  cand[0].click();
  return JSON.stringify({ok:true});
})()`

	jsPickModel = `(() => {
  const sleep = ms => new Promise(r => setTimeout(r, ms));
  return (async () => {
    const btns = [...document.querySelectorAll('button,[role="button"],[role="combobox"]')];
    const picker = btns.find(e => /gpt-6/i.test((e.getAttribute('aria-label')||'') + ' ' + (e.textContent||'')));
    if (!picker) return JSON.stringify({ok:false, err:'找不到模型选择器'});
    picker.click();
    await sleep(450);
    const items = [...document.querySelectorAll('[role="menuitem"],[role="option"],li,button,div')]
      .filter(e => e.childElementCount <= 3 && e.textContent.trim() === 'GPT-6 Luna');
    if (!items.length) return JSON.stringify({ok:false, err:'菜单里没有 GPT-6 Luna'});
    items[0].click();
    return JSON.stringify({ok:true});
  })().catch(e => JSON.stringify({ok:false, err:String(e)}));
})()`

	jsTypeComposer = `(() => {
  const el = document.querySelector('[placeholder*="Do anything" i],[data-placeholder*="Do anything" i],[aria-label*="Do anything" i]')
    || [...document.querySelectorAll('[contenteditable="true"]')].pop();
  if (!el) return JSON.stringify({ok:false, err:'找不到输入框'});
  el.focus();
  document.execCommand('insertText', false, __KICKOFF__);
  return JSON.stringify({ok:true, len:(el.textContent||el.value||'').length});
})()`

	jsSend = `(() => {
  const b = [...document.querySelectorAll('button')]
    .find(e => /^(send|发送)$/i.test(e.getAttribute('aria-label')||'') || /^(send|发送)$/i.test(e.textContent||''));
  if (!b) return JSON.stringify({ok:false, err:'找不到发送按钮'});
  b.click();
  return JSON.stringify({ok:true});
})()`
)

// DispatchCDP performs the whole opening flow through the DevTools channel.
// No OS focus changes, no pointer movement.
func (d *Dispatcher) DispatchCDP(ctx context.Context, kickoff string) ([]DispatchLog, error) {
	dispatchMu.Lock()
	defer dispatchMu.Unlock()
	var log []DispatchLog
	step := func(name string, ok bool, note string) {
		log = append(log, DispatchLog{Step: name, OK: ok, Note: note})
	}

	wsURL, relaunched, err := d.ensureCDP(ctx)
	if err != nil {
		return log, err
	}
	if relaunched {
		step("带调试端口重启 Codex", true, "后台通道就绪")
	}

	sleep := func(ms int) {
		t := time.NewTimer(time.Duration(ms) * time.Millisecond)
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}

	if ok, note, err := d.jsStep(ctx, wsURL, "新建对话", jsNewChat); ok {
		step("新建对话", true, note)
	} else {
		step("新建对话", false, note)
		return log, wrapErr("新建对话", err, note)
	}
	sleep(900)

	if ok, note, err := d.jsStep(ctx, wsURL, "选择模型", jsPickModel); ok {
		step("选择模型 "+d.Model, true, note)
	} else {
		step("选择模型 "+d.Model, false, note)
		return log, wrapErr("选择模型", err, note)
	}
	sleep(600)

	expr := strings.Replace(jsTypeComposer, "__KICKOFF__", jsString(kickoff), 1)
	if ok, note, err := d.jsStep(ctx, wsURL, "输入开工包", expr); ok {
		step("输入开工包", true, note)
	} else {
		step("输入开工包", false, note)
		return log, wrapErr("输入开工包", err, note)
	}
	sleep(700)

	if ok, note, err := d.jsStep(ctx, wsURL, "发送", jsSend); ok {
		step("发送", true, note)
	} else {
		step("发送", false, note)
		return log, wrapErr("发送", err, note)
	}
	return log, nil
}

func wrapErr(step string, err error, note string) error {
	if err != nil {
		return fmt.Errorf("%s: %v", step, err)
	}
	if note != "" {
		return fmt.Errorf("%s: %s", step, note)
	}
	return fmt.Errorf("%s 失败", step)
}

// jsString renders s as a JavaScript string literal (JSON-compatible).
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
