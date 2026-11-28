package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func knowledgeSourceFixture(t *testing.T) (*preview, <-chan map[string]any) {
	t.Helper()
	requests := make(chan map[string]any, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/knowledge/sources" && (r.Method == "POST" || r.Method == "PUT") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			requests <- body
			fmt.Fprint(w, `{"id":"source-1"}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	a := alignmentPreview(t)
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.Live.Connected = true
	return a, requests
}

func TestNativeKnowledgeSourceFolderPickerFillsHostPath(t *testing.T) {
	a, requests := knowledgeSourceFixture(t)
	options := make(chan mygo.OpenDialogOptions, 2)
	var picks atomic.Int32
	a.Live.PickDirectory = func(o mygo.OpenDialogOptions) ([]string, error) {
		picks.Add(1)
		options <- o
		return []string{"/Users/weijian/agent-lessons"}, nil
	}
	a.editKnowledgeSource(nativeKnowledgeSource{Name: "agent-lessons"})
	a.Settings.Open = true
	a.Settings.Tab = "memory-knowledge"
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "选择目录")
	got := <-options
	if !got.Directory || got.Multiple || got.ButtonLabel != "选择" {
		t.Fatalf("wrong native picker: %+v", got)
	}
	waitNative(t, a, func() bool { return a.Settings.Business.Source.Path == "/Users/weijian/agent-lessons" })
	if picks.Load() != 1 {
		t.Fatal("duplicate picker")
	}
	click(t, tt, "保存来源")
	waitNative(t, a, func() bool { return !a.Settings.Business.SourceEditor })
	body := <-requests
	if str(body["path"]) != "/Users/weijian/agent-lessons" || str(body["name"]) != "agent-lessons" {
		t.Fatalf("saved wrong source: %v", body)
	}
}

func TestNativeKnowledgeSourceFolderPickerCancelKeepsTypedPath(t *testing.T) {
	a, requests := knowledgeSourceFixture(t)
	a.Live.PickDirectory = func(mygo.OpenDialogOptions) ([]string, error) { return nil, nil }
	a.editKnowledgeSource(nativeKnowledgeSource{Name: "手工目录", Path: "/host/typed"})
	a.Settings.Open = true
	a.Settings.Tab = "memory-knowledge"
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "选择目录")
	waitNative(t, a, func() bool { return !a.Live.Loading["source-folder-picker"] })
	if a.Settings.Business.Source.Path != "/host/typed" {
		t.Fatalf("cancel overwrote typed path: %q", a.Settings.Business.Source.Path)
	}
	select {
	case <-requests:
		t.Fatal("cancel saved the source")
	default:
	}
}

func TestNativeKnowledgeSourceFolderPickerStaysLocalOnly(t *testing.T) {
	a, _ := knowledgeSourceFixture(t)
	a.Settings.Hosts = append(a.Settings.Hosts[:1], demoHost{ID: "remote", Name: "开发主机"})
	a.Settings.Host = 1
	var picks atomic.Int32
	a.Live.PickDirectory = func(mygo.OpenDialogOptions) ([]string, error) {
		picks.Add(1)
		return []string{"/picked/on/this/mac"}, nil
	}
	a.editKnowledgeSource(nativeKnowledgeSource{Name: "远程来源", Path: "/host/manual"})
	a.Settings.Open = true
	a.Settings.Tab = "memory-knowledge"
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "选择目录")
	waitNative(t, a, func() bool { return !a.Live.Loading["source-folder-picker"] })
	if picks.Load() != 0 {
		t.Fatal("remote host opened this Mac's folder picker")
	}
	if a.Settings.Business.Source.Path != "/host/manual" {
		t.Fatalf("remote pick changed the path: %q", a.Settings.Business.Source.Path)
	}
}
