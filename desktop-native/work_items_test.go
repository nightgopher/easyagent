package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestWorkInboxLayoutsAndReturnPreserveDraft(t *testing.T) {
	for _, width := range []int{900, 1240} {
		for _, theme := range []string{"dark", "light"} {
			t.Run(fmt.Sprintf("%d-%s", width, theme), func(t *testing.T) {
				a := alignmentPreview(t)
				a.Live.Connected = true
				a.Settings.Preferences.Theme = theme
				a.current().Draft = "尚未发送的补充需求"
				a.Live.Work = nativeWorkState{Open: true, View: "attention", Expanded: "one", NextPoll: time.Now().Add(time.Hour)}
				a.Live.Data["work"] = nativeWorkPage{Items: []nativeWorkItem{
					{ID: "one", SessionID: "layout", Title: "准备团队演示材料", State: "waiting_review", Attention: "waiting_review", Summary: "演示文稿已生成，讲解顺序和示例已核对。请查看最终版本。", Workspace: "/demo/easyagent", Artifacts: []nativeWorkArtifact{{Title: "演示文稿", Kind: "file", Value: "output/团队演示.pdf"}}, Version: 1},
					{ID: "two", Title: "整理客户反馈", State: "waiting_answer", Attention: "waiting_answer", Question: "本周报告需要覆盖全部客户，还是仅试点客户？", Workspace: "/demo/reports", Version: 2},
				}}
				tt := ui.NewTester(a.View, width, 820)
				for _, label := range []string{"搜索任务", "回到会话：准备团队演示材料", "标记完成：准备团队演示材料", "复制成果：演示文稿"} {
					box, ok := tt.Find(label)
					if !ok || box.W > 190 || box.X+box.W > float32(width) {
						t.Fatalf("action missing or overflowing %s: %+v", label, box)
					}
				}
				if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
					saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("work-%d-%s.png", width, theme)))
				}
				click(t, tt, "返回对话")
				if a.Live.Work.Open || a.current().Draft != "尚未发送的补充需求" {
					t.Fatal("return lost the conversation draft")
				}
			})
		}
	}
}

func TestWorkTrackingAndConflictKeepOriginalConversation(t *testing.T) {
	var mu sync.Mutex
	item := nativeWorkItem{ID: "work-one", SessionID: "owner", Title: "交付报告", State: "active", Version: 1}
	conflict := false
	requests := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/work" && r.Method == "POST":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			requests <- body
			_ = json.NewEncoder(w).Encode(item)
		case r.URL.Path == "/work" && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(nativeWorkPage{Items: []nativeWorkItem{item}})
		case r.Method == "PUT":
			if conflict {
				w.WriteHeader(409)
				fmt.Fprint(w, `{"error":"configuration changed; reload before saving"}`)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			requests <- body
			item.State = body["state"].(string)
			item.Version++
			_ = json.NewEncoder(w).Encode(item)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.Live.Connected = true
	a.current().RemoteID, a.current().Title, a.current().Draft = "owner", "交付报告", "保留草稿"
	a.trackSession(a.current())
	waitNative(t, a, func() bool { return a.Live.Work.Open && a.Live.Data["work"] != nil })
	body := <-requests
	if len(body) != 2 || body["session_id"] != "owner" {
		t.Fatalf("wrong tracking payload: %#v", body)
	}
	page := businessDecode[nativeWorkPage](a.Live.Data["work"])
	a.updateWorkState(page.Items[0], "completed")
	waitNative(t, a, func() bool {
		return !a.Live.Loading["work-save"] && a.Live.Data["work"] != nil && businessDecode[nativeWorkPage](a.Live.Data["work"]).Items[0].State == "completed"
	})
	body = <-requests
	if body["version"] != float64(1) || body["session_id"] != nil {
		t.Fatalf("wrong update payload: %#v", body)
	}
	mu.Lock()
	conflict = true
	mu.Unlock()
	page = businessDecode[nativeWorkPage](a.Live.Data["work"])
	a.updateWorkState(page.Items[0], "active")
	waitNative(t, a, func() bool { return a.Live.ResourceErrors["work-save"] != "" })
	if a.current().Draft != "保留草稿" || businessDecode[nativeWorkPage](a.Live.Data["work"]).Items[0].State != "completed" {
		t.Fatal("conflicting write changed visible task or draft")
	}
}

func TestWorkReadsCancelWhenPageCloses(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/work" {
			fmt.Fprint(w, `{}`)
			return
		}
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer srv.Close()
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.Live.Connected = true
	a.openWork()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("no work request")
	}
	a.closeWork()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("work request was not cancelled")
	}
	a.liveTick()
	if a.Live.ResourceErrors["work"] != "" {
		t.Fatal("intentional cancellation surfaced as error")
	}
}

func TestNativeCoreWorkTrackingLifecycle(t *testing.T) {
	a, _, _, _ := nativeCoreFixture(t)
	a.createLiveSession("")
	waitNative(t, a, func() bool { return a.current().RemoteID != "" && !a.Live.Loading["create-session"] })
	a.current().Draft = "未发送内容"
	a.trackSession(a.current())
	waitNative(t, a, func() bool {
		return a.Live.Work.Open && a.Live.Data["work"] != nil && len(businessDecode[nativeWorkPage](a.Live.Data["work"]).Items) == 1
	})
	item := businessDecode[nativeWorkPage](a.Live.Data["work"]).Items[0]
	if item.SessionID != a.current().RemoteID {
		t.Fatal("tracked the wrong session")
	}
	a.Live.Work.View = "all"
	a.updateWorkState(item, "completed")
	waitNative(t, a, func() bool {
		return a.Live.Data["work"] != nil && businessDecode[nativeWorkPage](a.Live.Data["work"]).Items[0].State == "completed"
	})
	a.closeWork()
	a.openWork()
	waitNative(t, a, func() bool { return a.Live.Data["work"] != nil })
	if a.current().Draft != "未发送内容" || businessDecode[nativeWorkPage](a.Live.Data["work"]).Items[0].State != "completed" {
		t.Fatal("tracked work or draft was not preserved")
	}
}
