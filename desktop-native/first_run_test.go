package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestFirstRunModelNoticeAndSettingsEntry(t *testing.T) {
	var configured atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/models" {
			if configured.Load() {
				_, _ = fmt.Fprint(w, `{"source":"configured","models":[{"id":"demo"}],"current":{"id":"demo","provider":"openai"}}`)
			} else {
				_, _ = fmt.Fprint(w, `{"source":"unconfigured","models":[]}`)
			}
			return
		}
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Live.Client, _ = newBackendClient(server.URL, "")
	a.Live.Connected = true
	a.refreshModels()
	waitNative(t, a, func() bool { return !a.Live.Loading["model-list"] })
	if a.Live.Notice != unconfiguredModelNotice {
		t.Fatalf("missing setup notice: %q", a.Live.Notice)
	}
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "配置模型")
	if !a.Settings.Open || a.Settings.Tab != "models" {
		t.Fatal("setup entry did not open model settings")
	}
	waitNative(t, a, func() bool { return !a.Live.Loading["model-list"] })
	configured.Store(true)
	a.refreshModels()
	waitNative(t, a, func() bool { return !a.Live.Loading["model-list"] })
	if a.Live.Notice == unconfiguredModelNotice {
		t.Fatal("setup notice remains after configuration")
	}
}
