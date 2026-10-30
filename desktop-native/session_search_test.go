package main

import (
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func nativeSearchFixture(t *testing.T, handler http.HandlerFunc) *preview {
	t.Helper()
	a := alignmentPreview(t)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	b, err := newBackendClient(srv.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	a.Live.Client, a.Live.Connected, a.Live.hostCtx = b, true, a.Live.ctx
	a.Live.LastSave = time.Now()
	return a
}

func TestSessionSearchDebounceFiltersPaginationAndEmpty(t *testing.T) {
	var count atomic.Int32
	requests := make(chan string, 8)
	a := nativeSearchFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/search" {
			fmt.Fprint(w, "{}")
			return
		}
		count.Add(1)
		requests <- r.URL.RawQuery
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing host credentials")
		}
		result := sessionSearchResponse{Total: 61, Hits: []sessionSearchHit{{SessionID: "cold", Title: "旧会话", EntryID: "anchor", Role: "assistant", Snippet: "修复迷你主机连接", MatchStart: 2, MatchEnd: 6, Workspace: "/demo/easyagent"}}}
		if r.URL.Query().Get("q") == "不存在" {
			result = sessionSearchResponse{}
		}
		if err := json.NewEncoder(w).Encode(result); err != nil {
			t.Error(err)
		}
	})
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "搜索会话")
	tt.Type("迷你主机")
	if count.Load() != 0 {
		t.Fatal("request issued before debounce")
	}
	a.Live.SessionSearch.Due = time.Now().Add(-time.Second)
	waitNative(t, a, func() bool { return a.Live.SessionSearch.Loaded })
	tt.Frame()
	if !tt.HasText("旧会话") || !tt.HasText("61 条匹配") {
		t.Fatal("cold history matches not rendered")
	}
	first := <-requests
	if !strings.Contains(first, "limit=30") || strings.Contains(first, "archived=") {
		t.Fatalf("default search did not include archive: %s", first)
	}
	a.Live.SessionSearch.Workspace, a.Live.SessionSearch.Archive, a.Live.SessionSearch.Period = "/demo/easyagent", "已归档", "最近 7 天"
	a.queueSessionSearch()
	a.searchSessionPage(30)
	waitNative(t, a, func() bool { return !a.Live.SessionSearch.Loading })
	filtered := <-requests
	for _, part := range []string{"archived=true", "offset=30", "since=", "workspace=%2Fdemo%2Feasyagent"} {
		if !strings.Contains(filtered, part) {
			t.Errorf("missing %s in %s", part, filtered)
		}
	}
	a.Search = "不存在"
	a.queueSessionSearch()
	a.searchSessionPage(0)
	waitNative(t, a, func() bool { return !a.Live.SessionSearch.Loading })
	tt.Frame()
	if !tt.HasText("没有匹配的会话内容") {
		t.Fatal("missing empty result explanation")
	}
	click(t, tt, "清除会话搜索")
	if a.Search != "" || !tt.HasText("最近") || len(a.Live.SessionSearch.Result.Hits) != 0 {
		t.Fatal("clear did not restore normal sidebar")
	}
}

func TestSessionSearchRejectsLateQueryAndHostResponses(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	a := nativeSearchFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "old" {
			started <- struct{}{}
			<-release
		}
		_ = json.NewEncoder(w).Encode(sessionSearchResponse{Total: 1, Hits: []sessionSearchHit{{Title: r.URL.Query().Get("q")}}})
	})
	a.Search = "old"
	a.queueSessionSearch()
	a.searchSessionPage(0)
	<-started
	a.Search = "new"
	a.queueSessionSearch()
	a.searchSessionPage(0)
	waitNative(t, a, func() bool { return a.Live.SessionSearch.Loaded })
	close(release)
	for range 10 {
		a.liveTick()
		time.Sleep(time.Millisecond)
	}
	if a.Live.SessionSearch.Result.Hits[0].Title != "new" {
		t.Fatal("late query replaced newer results")
	}
	oldEpoch := a.Live.epoch
	a.post(oldEpoch, func() { a.Live.SessionSearch.Result = sessionSearchResponse{Total: 999} })
	a.Live.epoch++
	a.resetSessionSearch()
	a.Search = ""
	a.liveTick()
	if a.Live.SessionSearch.Result.Total != 0 {
		t.Fatal("old host search survived switch")
	}
}

func TestSessionSearchOlderHostAndDisconnectedFeedback(t *testing.T) {
	a := nativeSearchFixture(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	a.Search = "令牌"
	a.queueSessionSearch()
	a.searchSessionPage(0)
	waitNative(t, a, func() bool { return a.Live.SessionSearch.Loaded })
	tt := ui.NewTester(a.View, 900, 680)
	if !strings.Contains(a.Live.SessionSearch.Error, "请升级") {
		t.Fatal(a.Live.SessionSearch.Error)
	}
	if !tt.HasText("重新搜索") {
		t.Fatal("missing retry")
	}
	a.Live.Connected = false
	a.searchSessionPage(0)
	if !strings.Contains(a.Live.SessionSearch.Error, "连接当前主机") {
		t.Fatal("offline search looked empty")
	}
}

func TestSessionSearchOpensAnchorAndCompactedOriginal(t *testing.T) {
	a := nativeSearchFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/history") {
			fmt.Fprint(w, `{"messages":[{"entry_id":"original","role":"assistant","text":"压缩前的迷你主机令牌说明","timestamp":1793300000000}]}`)
			return
		}
		fmt.Fprint(w, "{}")
	})
	s := a.current()
	s.Draft = "尚未发送的草稿"
	search := &a.Live.SessionSearch
	search.Query = "令牌"
	hit := sessionSearchHit{SessionID: s.RemoteID, EntryID: "target", Title: "标题", Role: "assistant"}
	search.Target, search.TargetQuery = &hit, "令牌"
	a.receive(envelope{Type: "snapshot", SessionID: s.RemoteID, Seq: 1, Messages: []storedMessage{
		{Role: "assistant", EntryID: "earlier", Content: "另一个令牌"}, {Role: "assistant", EntryID: "target", Content: "需要找的令牌"},
	}})
	if !s.FindOpen || !s.FindJump || s.FindIndex != 1 || search.History != nil {
		t.Fatal("did not select the matched entry")
	}
	hit.EntryID = "original"
	search.Target = &hit
	a.receive(envelope{Type: "snapshot", SessionID: s.RemoteID, Seq: 2, Messages: []storedMessage{{Role: "assistant", EntryID: "summary", Content: "当前压缩摘要"}}})
	waitNative(t, a, func() bool { return search.History != nil && !search.History.Loading })
	tt := ui.NewTester(a.View, 1240, 820)
	if !tt.HasText("历史片段 · 只读") {
		t.Fatal("missing original-history reader")
	}
	if _, ok := tt.Find("消息输入"); ok {
		t.Fatal("history reader exposes an ambiguous composer")
	}
	if len(s.Messages) != 1 || s.Messages[0].EntryID != "summary" || s.Draft != "尚未发送的草稿" {
		t.Fatal("history read modified live context or draft")
	}
	click(t, tt, "返回当前对话")
	if search.History != nil {
		t.Fatal("history reader did not close")
	}
	if _, ok := tt.Find("消息输入"); !ok {
		t.Fatal("composer not restored")
	}
}

func TestSessionSearchClickLoadsUnopenedSession(t *testing.T) {
	a := nativeSearchFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sessions/cold/run" {
			fmt.Fprint(w, `{"type":"snapshot","session_id":"cold","seq":1,"messages":[{"role":"assistant","entry_id":"target","content":"此前保存的令牌说明"}]}`)
			return
		}
		fmt.Fprint(w, "{}")
	})
	previous := a.current()
	previous.Draft = "保留原会话草稿"
	a.Search = "令牌"
	a.Live.SessionSearch = nativeSessionSearch{Query: a.Search, Loaded: true, Result: sessionSearchResponse{Total: 1, Hits: []sessionSearchHit{{SessionID: "cold", EntryID: "target", Title: "未打开的会话", Role: "assistant", Snippet: "此前保存的令牌说明"}}}}
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "打开搜索结果：未打开的会话 / target")
	waitNative(t, a, func() bool { return a.current().RemoteID == "cold" && a.current().FindOpen })
	s := a.current()
	if len(s.Messages) != 1 || s.Messages[0].EntryID != "target" || s.FindQuery != "令牌" || previous.Draft != "保留原会话草稿" {
		t.Fatal("click did not load and locate the persisted message safely")
	}
}

func TestSessionSearchLayoutAndShortcut(t *testing.T) {
	for _, width := range []int{900, 1240} {
		for _, theme := range []string{"dark", "light"} {
			a := alignmentPreview(t)
			a.Live.Connected = true
			a.Search = "迷你主机"
			a.Settings.Preferences.Theme = theme
			a.Live.SessionSearch = nativeSessionSearch{Query: a.Search, Loaded: true, Result: sessionSearchResponse{Total: 2, Hits: []sessionSearchHit{
				{SessionID: "cold", EntryID: "answer", Title: "修复远程主机连接与凭据保存", Role: "assistant", Snippet: "更新后连接失败，已修复迷你主机访问令牌的读取流程。", MatchStart: 12, MatchEnd: 16, Workspace: "/demo/easyagent", UpdatedAt: 1793300000000},
				{SessionID: "archive", EntryID: "question", Title: "远程部署记录", Role: "user", Snippet: "怎么让迷你主机上的 EasyAgent 常驻运行？", MatchStart: 3, MatchEnd: 7, Workspace: "/demo/easyagent", Archived: true, UpdatedAt: 1793200000000},
			}}}
			tt := ui.NewTester(a.View, width, 820)
			for _, label := range []string{"搜索会话", "搜索项目", "搜索归档范围", "搜索时间范围", "打开搜索结果：修复远程主机连接与凭据保存 / answer"} {
				r, ok := tt.Find(label)
				if !ok || r.X < 0 || r.X+r.W > 260 || r.W < 30 {
					t.Fatalf("%d %s: %s overflow or missing: %+v", width, theme, label, r)
				}
			}
			if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("session-search-%d-%s.png", width, theme)
				f, err := os.Create(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(f, tt.Image())
				closeErr := f.Close()
				if err != nil {
					t.Fatal(err)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			a.Sidebar = false
			tt.Frame()
			tt.Key(ui.Cmd|ui.Shift, ui.KeyF)
			if !a.Sidebar {
				t.Fatal("global search shortcut did not reveal sidebar")
			}
		}
	}
}
