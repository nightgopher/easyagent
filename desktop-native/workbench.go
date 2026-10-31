package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/gorilla/websocket"
)

type nativeFile struct {
	RestoredPreview                            *bool
	Kind, Error                                string
	Loading, Editing                           bool
	Data                                       []byte
	SVG                                        *ui.SVG
	Scroll                                     ui.ScrollState
	Markdown                                   markdownDocument
	PDFPage, PDFCount                          int
	Zoom                                       float32
	Path, Original, SessionID, Workspace, Host string
	Code                                       *editor.State
	Preview                                    bool
	Image                                      *ui.Bitmap
}
type nativeWorkbench struct {
	Capture                                                                          captureWorkbench
	KBCloud                                                                          kbTagCloud
	KBPaneWidth                                                                      float32
	KBActiveTag                                                                      string
	KBList                                                                           ui.ListState
	KBTags                                                                           ui.ListState
	KBSearchAt                                                                       time.Time
	SavedFiles                                                                       map[string]fileWorkspace
	RestoredFiles                                                                    map[string]bool
	KBView, KBCategory, KBTag                                                        string
	PendingReviewPath                                                                string
	Scope                                                                            string
	FileScope                                                                        string
	ShowTree, FileOpenMenu, SearchLoaded, SearchLoading                              bool
	Directories                                                                      map[string]*nativeDirectory
	SearchFiles                                                                      []string
	CompactInstructions                                                              string
	Tab, Directory, Search, ActiveFile, SelectedRun, SelectedChange, KBQuery, KBPath string
	KBEntry                                                                          map[string]any
	Files                                                                            map[string]*nativeFile
	Tabs                                                                             []string
	Diff                                                                             *editor.DiffState
	// Terminals are the open shells; TerminalOpen shows the panel.
	Terminals                      []*terminalTab
	ActiveTerminal, NextTerminalID int
	TerminalOpen                   bool
	// BackgroundTask is the task whose output the 「后台」 page shows.
	BackgroundTask    string
	BackgroundFetched time.Time
}

func newNativeWorkbench() nativeWorkbench {
	return nativeWorkbench{Tab: "files", Files: map[string]*nativeFile{}, SavedFiles: map[string]fileWorkspace{}, RestoredFiles: map[string]bool{}}
}

// close ends every shell; used when the host connection is replaced.
func (w *nativeWorkbench) close() {
	for _, t := range w.Terminals {
		t.close()
	}
	w.Terminals = nil
	w.ActiveTerminal = 0
	w.TerminalOpen = false
}
func (a *preview) workspacePath(resource, path, sid string) string {
	return "/workspace/" + resource + query(map[string]string{"path": path, "session_id": sid})
}
func (a *preview) openResource(tab string) {
	a.Workbench = true
	a.Live.Workbench.Tab = tab
	a.refreshWorkbench()
}
func (a *preview) refreshWorkbench() {
	a.bindWorkbench()
	l := a.Live
	w := &l.Workbench
	s := a.current()
	if l.Client == nil {
		return
	}
	switch w.Tab {
	case "inbox":
		a.resource("inbox", "/inbox")
	case "files":
		a.bindFileScope()
		w.Directories = map[string]*nativeDirectory{}
		w.SearchLoaded = false
		a.readNativeDirectory(s.Workspace)
	case "review":
		if s.RemoteID != "" {
			a.resource("review", sessionPath(s.RemoteID)+"/run-files")
		}
	case "context":
		if s.RemoteID != "" {
			a.resource("context", sessionPath(s.RemoteID)+"/context")
		}
	case "background":
		a.loadBackgroundTasks(s)
	case "skills":
		if s.RemoteID != "" {
			a.resource("capabilities", sessionPath(s.RemoteID)+"/capabilities")
		}
	case "tools":
		a.resource("tools", "/tools")
	case "profile":
		if s.AgentID != "" {
			delete(l.Data, "business-context-memory")
			a.resource("business-context-memory", "/agents/"+s.AgentID+"/memories")
			return
		}
		a.resource("profile", "/profile")
	case "kb":
		if s.AgentID != "" {
			delete(l.Data, "business-context-document")
			delete(l.Data, "business-context-knowledge")
			a.resource("business-context-knowledge", "/agents/"+s.AgentID+"/knowledge"+query(map[string]string{"q": w.KBQuery}))
			return
		}
		if w.KBView == "tags" {
			if w.KBActiveTag != "" {
				a.resource("kb-tag-entries", "/kb/entries"+query(map[string]string{"tag": w.KBActiveTag}))
			}
		} else if w.KBView == "" {
			a.resource("kb", "/kb/entries"+query(map[string]string{"q": w.KBQuery, "category": w.KBCategory, "tag": w.KBTag}))
		}
		if l.Data["kb-stats"] == nil {
			a.resource("kb-stats", "/kb/stats")
		}
		if l.Data["kb-categories"] == nil {
			a.resource("kb-categories", "/kb/categories")
		}
		if w.KBView == "tags" && l.Data["kb-tags"] == nil {
			a.resource("kb-tags", "/kb/tags")
		}
		if w.KBView == "health" && l.Data["kb-health"] == nil {
			a.resource("kb-health", "/kb/health")
		}
	}
}

var workbenchTabs = []struct{ id, label, icon string }{
	{"inbox", "收件箱", "file"}, {"artifact", "成果草稿", "file"}, {"review", "审查", "git-branch"}, {"files", "文件", "folder"}, {"plan", "计划", "file"}, {"tasks", "任务", "check"}, {"background", "后台", "terminal"}, {"kb", "知识库", "book"}, {"profile", "记忆", "user"}, {"skills", "技能", "code"}, {"context", "上下文", "activity"}, {"tools", "工具", "wrench"},
}

func (a *preview) liveWorkbench(c *ui.Context) {
	a.bindWorkbench()
	l := a.Live
	w := &l.Workbench
	title := "工作台"
	for _, tab := range workbenchTabs {
		if tab.id == w.Tab {
			title = tab.label
		}
	}
	width, _ := c.Size()
	maxWidth := width - 320
	if a.Sidebar && width >= 1100 {
		maxWidth -= 260
	}
	maxWidth = min(900, maxWidth)
	paneWidth := a.Settings.Preferences.WorkbenchWidth
	if paneWidth < 340 {
		paneWidth = 440
	}
	paneWidth = max(340, min(paneWidth, maxWidth))
	ui.Row(c).Width(paneWidth).MinWidth(0).AlignItems(ui.Stretch).Background(c.Theme().Background).BorderWidth(0, 0, 0, 1).BorderColor(c.Theme().Border).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Row(c).Height(44).Padding(0, 12).Gap(8).AlignItems(ui.Center).Children(func() {
				a.text(c, title).FontSize(13).FontWeight(500)
				ui.Spacer(c)
				iconButton(c, "refresh", "刷新工作台").OnClick(func() {
					if l != nil && l.Workbench.Tab == "kb" {
						delete(l.Data, "kb-stats")
						delete(l.Data, "kb-categories")
						delete(l.Data, "kb-tags")
						delete(l.Data, "kb-health")
					}
					a.refreshWorkbench()
				})
				iconButton(c, "x", "关闭工作台").OnClick(func() { a.Workbench = false })
			})
			switch w.Tab {
			case "inbox":
				a.inboxWorkbench(c)
			case "artifact":
				a.artifactWorkbench(c)
			case "files":
				a.filesWorkbench(c)
			case "review":
				a.reviewWorkbench(c)
			case "tasks", "plan":
				a.tasksWorkbench(c)
			case "background":
				a.backgroundWorkbench(c)
			case "context":
				a.contextWorkbench(c)
			case "tools":
				ui.Scroll(c).Grow(1).MinHeight(0).Padding(12).Children(func() { ui.Text(c, pretty(l.Data[w.Tab])).Font("monospace").FontSize(11).LineHeight(1.5).Selectable() })
			case "skills":
				a.skillsWorkbench(c)
			case "profile":
				a.profileWorkbench(c)
			case "kb":
				w.KBPaneWidth = paneWidth - 45
				a.kbWorkbench(c)
			}
		})
		ui.Column(c).Width(44).Padding(6, 4).Gap(2).BorderWidth(0, 0, 0, 1).BorderColor(c.Theme().Border).Children(func() {
			for _, tab := range workbenchTabs[:8] {
				button := iconButton(c.Key(tab.id), tab.icon, tab.label).Size(36, 34).OnClick(func() { a.openResource(tab.id) })
				if w.Tab == tab.id {
					button.TextColor(c.Theme().Accent).Background(c.Theme().SurfaceHover)
				}
			}
		})
		a.resizeEdge(c, "调整工作台宽度", false, paneWidth, 340, maxWidth, func(value float32) { a.Settings.Preferences.WorkbenchWidth = value; a.Settings.savePreferences() })
	})
}
func (a *preview) liveTerminal(c *ui.Context) {
	w := &a.Live.Workbench
	if !w.TerminalOpen {
		return
	}
	_, windowHeight := c.Size()
	height := a.Settings.Preferences.TerminalHeight
	if height < 120 {
		height = 280
	}
	height = min(height, max(120, windowHeight-400))
	active := w.activeTerminal()
	ui.Column(c).Height(height).MinHeight(120).BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
		ui.Row(c).Height(32).Padding(0, 8, 0, 12).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Label("终端标签").Gap(2).Grow(1).MinWidth(0).Clip().Children(func() {
				for _, tab := range w.Terminals {
					a.terminalTabButton(c.Key(tab.ID), tab, tab == active)
				}
			})
			iconButton(c, "plus", "新建终端").Size(26, 26).Disabled(len(w.Terminals) >= maxTerminalTabs).OnClick(func() { a.newTerminal() })
			iconButton(c, "x", "收起终端").Tooltip("收起终端（shell 保持运行）").Size(26, 26).OnClick(func() { w.TerminalOpen = false })
		})
		if active == nil {
			ui.Row(c).Padding(4, 12).Children(func() { a.note(c, "没有打开的终端") })
		} else if active.Term != nil {
			select {
			case <-active.Term.Done():
				ui.Row(c).Gap(8).Padding(4, 12).Children(func() {
					a.note(c, "终端已断开")
					a.action(c, "重新连接终端").OnClick(func() { w.closeTerminal(active); a.newTerminal() })
				})
			default:
			}
			terminal.View(c.Key(active.ID), active.Term).Grow(1).FillWidth()
		} else if active.Error != "" {
			ui.Column(c).Gap(6).Padding(4, 12).Children(func() {
				a.note(c, active.Error)
				a.action(c, "重新连接终端").AlignSelf(ui.Start).OnClick(func() { w.closeTerminal(active); a.newTerminal() })
			})
		} else {
			ui.Row(c).Padding(4, 12).Children(func() { a.note(c, "正在连接终端…") })
		}
		a.resizeEdge(c, "调整终端高度", true, height, 120, min(720, windowHeight-400), func(value float32) { a.Settings.Preferences.TerminalHeight = value; a.Settings.savePreferences() })
	})
}

func (a *preview) terminalTabButton(c *ui.Context, tab *terminalTab, selected bool) {
	w := &a.Live.Workbench
	ui.Row(c).Gap(0).AlignItems(ui.Center).Radius(5).Background(func() ui.Color {
		if selected {
			return c.Theme().SurfaceHover
		}
		return ui.Color{}
	}()).Children(func() {
		button := ui.ButtonBase(c).Label("切换终端："+tab.title()).Padding(4, 8).OnClick(func() { w.ActiveTerminal = tab.ID })
		button.Children(func() {
			text := ui.Text(c, tab.title()).FontSize(11).SingleLine()
			if !selected {
				text.TextColor(c.Theme().TextMuted)
			}
		})
		iconButton(c, "x", "关闭终端："+tab.title()).Tooltip("关闭此终端（结束 shell）").Size(20, 20).OnClick(func() { w.closeTerminal(tab) })
	})
}

func (a *preview) saveNativeFile(f *nativeFile) {
	if f.Host != a.Live.Config.ActiveHost {
		a.Live.Error = "文件属于其他运行主机，未写入"
		return
	}
	l := a.Live
	text := f.Code.Buffer.String()
	original := f.Original
	path := a.workspacePath("read-file", f.Path, f.SessionID)
	write := a.workspacePath("write-file", f.Path, f.SessionID)
	a.run("file-save", func(ctx context.Context, b *backendClient) (any, error) {
		var current map[string]any
		if err := b.request(ctx, "GET", path, nil, &current); err != nil {
			return nil, err
		}
		if str(current["content"]) != original {
			return nil, errors.New("文件已在主机上改变，未覆盖；请关闭后重新打开并比较")
		}
		return nil, b.request(ctx, "PUT", write, map[string]any{"content": text}, nil)
	}, func(any) {
		f.Original = text
		f.Editing = false
		if f.Code.Buffer.String() == text {
			f.Code.Buffer.MarkSaved()
		}
		l.Notice = "文件已保存。"
	})
}
func (a *preview) closeNativeFile(key string) {
	w := &a.Live.Workbench
	delete(w.Files, key)
	for i, k := range w.Tabs {
		if k == key {
			w.Tabs = append(w.Tabs[:i], w.Tabs[i+1:]...)
			break
		}
	}
	w.ActiveFile = ""
	if len(w.Tabs) > 0 {
		for i := len(w.Tabs) - 1; i >= 0; i-- {
			key := w.Tabs[i]
			f := w.Files[key]
			if f.Host == a.Live.Config.ActiveHost && f.Workspace == a.current().Workspace {
				w.ActiveFile = key
				break
			}
		}
	}
}
func (a *preview) exportNativeFile(f *nativeFile) {
	if f.Host != a.Live.Config.ActiveHost {
		a.Live.Error = "文件属于其他运行主机，未导出"
		return
	}
	path := a.workspacePath("file-data", f.Path, f.SessionID)
	a.background(func(ctx context.Context, b *backendClient) (any, error) {
		var m map[string]any
		if err := b.request(ctx, "GET", path, nil, &m); err != nil {
			return nil, err
		}
		data, err := base64.StdEncoding.DecodeString(str(m["data"]))
		if err != nil {
			return nil, err
		}
		target, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "导出文件", DefaultPath: filepath.Base(f.Path)})
		if err != nil || target == "" {
			return nil, err
		}
		return target, os.WriteFile(target, data, 0600)
	}, func(v any) {
		if str(v) != "" {
			a.Live.Notice = "文件导出已完成。"
		}
	})
}
func (a *preview) tasksWorkbench(c *ui.Context) {
	s := a.current()
	plan := a.Live.Workbench.Tab == "plan"
	ui.Scroll(c.Key(a.Live.Workbench.Tab + s.RemoteID)).Grow(1).MinHeight(0).Padding(14).Children(func() {
		ui.Column(c).Gap(12).Children(func() {
			if plan {
				done := 0
				for _, entry := range s.Plan {
					if entry.Status == "completed" {
						done++
					}
				}
				a.note(c, fmt.Sprintf("%d / %d 已完成", done, len(s.Plan)))
				if len(s.Plan) == 0 {
					a.note(c, "暂无执行计划。Agent 更新计划后会显示在这里。")
				}
				for _, entry := range s.Plan {
					glyph := "circle"
					color := c.Theme().TextMuted
					if entry.Status == "completed" {
						glyph = "check"
						color = c.Theme().Accent
					} else if entry.Status == "in_progress" {
						glyph = "activity"
						color = c.Theme().Text
					}
					ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
						ui.Box(c).TextColor(color).Children(func() { icon(c, glyph) })
						ui.Text(c, entry.Content).FontSize(12).Grow(1).MinWidth(0)
					})
				}
				return
			}
			running, total := 0, 0
			for _, m := range s.Messages {
				if m.Kind == "tool" {
					total++
					if m.Status == "执行中" || m.Status == "未完成" {
						running++
					}
				}
			}
			a.note(c, fmt.Sprintf("执行中 %d · 共 %d 项", running, total))
			if total == 0 {
				a.note(c, "暂无工具任务。")
			}
			for i := len(s.Messages) - 1; i >= 0; i-- {
				m := s.Messages[i]
				if m.Kind != "tool" {
					continue
				}
				glyph := "activity"
				color := c.Theme().TextMuted
				if m.Status == "完成" {
					glyph = "check"
					color = c.Theme().Accent
				} else if m.Status == "失败" {
					glyph = "alert"
					color = c.Theme().Danger
				}
				ui.Row(c.Key(m.CallID)).Gap(10).Children(func() {
					ui.Box(c).TextColor(color).Children(func() { icon(c, glyph) })
					ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() { ui.Text(c, m.Tool).FontSize(12).Font("monospace"); a.note(c, m.Status) })
				})
			}
		})
	})
}

// remoteTerminal implements the existing server's framed WS terminal protocol
// for MyGo's native terminal emulator, including output credit acknowledgments.
type remoteTerminal struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	pending []byte
}

func (t *remoteTerminal) writeJSON(v any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return t.conn.WriteJSON(v)
}
func (t *remoteTerminal) Read(p []byte) (int, error) {
	for len(t.pending) == 0 {
		kind, data, err := t.conn.ReadMessage()
		if err != nil {
			return 0, err
		}
		if kind == websocket.BinaryMessage {
			t.pending = data
		} else {
			var v map[string]any
			_ = json.Unmarshal(data, &v)
			if str(v["type"]) == "error" {
				return 0, errors.New(str(v["message"]))
			}
		}
	}
	n := copy(p, t.pending)
	t.pending = t.pending[n:]
	if len(t.pending) == 0 {
		if err := t.writeJSON(map[string]any{"type": "ack"}); err != nil {
			return n, err
		}
	}
	return n, nil
}
func (t *remoteTerminal) Write(p []byte) (int, error) {
	err := t.writeJSON(map[string]any{"type": "input", "data": string(p)})
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
func (t *remoteTerminal) Resize(cols, rows int) error {
	return t.writeJSON(map[string]any{"type": "resize", "cols": cols, "rows": rows})
}
func (t *remoteTerminal) Close() error { return t.conn.Close() }

// toggleTerminal shows or hides the terminal panel, opening a first shell
// for the current session when none exists.
func (a *preview) toggleTerminal() {
	w := &a.Live.Workbench
	if w.TerminalOpen {
		w.TerminalOpen = false
		return
	}
	if len(w.Terminals) > 0 {
		w.TerminalOpen = true
		return
	}
	a.newTerminal()
}

var _ io.ReadWriteCloser = (*remoteTerminal)(nil)

// terminalHandshakeError replaces the generic "websocket: bad handshake" with
// the status and error body the server actually returned, so an opt-in flag
// such as EA_ENABLE_TERMINAL is not mistaken for a protocol or network failure.
func terminalHandshakeError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	detail := strings.TrimSpace(string(body))
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		detail = payload.Error
	}
	if detail == "" {
		detail = http.StatusText(resp.StatusCode)
	}
	if strings.Contains(detail, "terminal is not enabled") {
		detail += "；需在远端核心 core.env 设置 EA_ENABLE_TERMINAL=1 并重启服务"
	}
	return fmt.Errorf("HTTP %d：%s", resp.StatusCode, detail)
}

// The same splitter handles pointer drags and keyboard resizing without changing
// the session, file buffers, or terminal connection being resized.
func (a *preview) resizeEdge(c *ui.Context, label string, vertical bool, value, minimum, maximum float32, save func(float32)) {
	maximum = max(minimum, maximum)
	edge := ui.Box(c.Key(label)).Label(label).Role(ui.RoleSplitter).Focusable().Absolute()
	if vertical {
		edge.Top(-3).Left(0).Height(6).FillWidth().Cursor(ui.CursorResizeNS)
	} else {
		edge.Left(-3).Top(0).Width(6).FillHeight().Cursor(ui.CursorResizeEW)
	}
	set := func(v float32) {
		next := max(minimum, min(v, maximum))
		if next != value {
			save(next)
		}
	}
	clickTime := ui.Local(edge, "clickTime", func() time.Time {
		return time.Time{}
	})
	edge.OnClick(func() {
		now := time.Now()
		if !clickTime.IsZero() && now.Sub(*clickTime) < 350*time.Millisecond {
			if !vertical {
				defaultWidth := float32(440)
				if defaultWidth < minimum || defaultWidth > maximum {
					defaultWidth = (minimum + maximum) / 2
				}
				set(defaultWidth)
			}
			*clickTime = time.Time{}
		} else {
			*clickTime = now
		}
	})
	if dx, dy, held := edge.Dragged(); held {
		if vertical {
			if dy < -4 || dy > 4 {
				set(value - dy)
			}
		} else {
			if value-dx < minimum-40 {
				a.Workbench = false
				return
			}
			if dx < -4 || dx > 4 {
				set(value - dx)
			}
		}
	}
	edge.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputKeyDown {
			return false
		}
		if !vertical && ev.Key == ui.KeyLeft || vertical && ev.Key == ui.KeyUp {
			set(value + 10)
			return true
		}
		if !vertical && ev.Key == ui.KeyRight || vertical && ev.Key == ui.KeyDown {
			set(value - 10)
			return true
		}
		return false
	})
}
