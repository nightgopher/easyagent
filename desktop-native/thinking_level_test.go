package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/egoist/mygo/ui"
)

func thinkingPickerFixture(t *testing.T) (*preview, <-chan map[string]any) {
	t.Helper()
	requests := make(chan map[string]any, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/sessions/layout/thinking" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			requests <- body
			mode, effort := "", ""
			if v, ok := body["mode"].(string); ok {
				mode = v
			}
			if v, ok := body["effort"].(string); ok {
				effort = v
			}
			fmt.Fprintf(w, `{"mode":%q,"effort":%q,"budget":0}`, mode, effort)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	a := alignmentPreview(t)
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.Live.Connected = true
	a.ModelMenu = true
	return a, requests
}

func TestSessionThinkingLevelPicker(t *testing.T) {
	a, requests := thinkingPickerFixture(t)
	tt := ui.NewTester(a.View, 1240, 820)

	click(t, tt, "思考强度：高")
	waitNative(t, a, func() bool { return a.current().ThinkingLevel == "high" })
	body := <-requests
	if str(body["effort"]) != "high" || body["mode"] != nil {
		t.Fatalf("wrong high body: %v", body)
	}

	click(t, tt, "思考强度：关")
	waitNative(t, a, func() bool { return a.current().ThinkingLevel == "off" })
	body = <-requests
	if str(body["mode"]) != "disabled" || body["effort"] != nil {
		t.Fatalf("wrong off body: %v", body)
	}

	click(t, tt, "思考强度：默认")
	waitNative(t, a, func() bool { return a.current().ThinkingLevel == "" })
	body = <-requests
	if len(body) != 0 {
		t.Fatalf("default must clear the override: %v", body)
	}
}

func TestUnsavedSessionThinkingStaysLocal(t *testing.T) {
	a, requests := thinkingPickerFixture(t)
	a.current().RemoteID = ""
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "思考强度：低")
	if a.current().ThinkingLevel != "low" {
		t.Fatalf("local level not stored: %q", a.current().ThinkingLevel)
	}
	select {
	case <-requests:
		t.Fatal("unsaved session must not POST")
	default:
	}
}

func TestThinkingLevelMappings(t *testing.T) {
	if got := thinkingBody("off"); str(got["mode"]) != "disabled" || got["effort"] != nil {
		t.Fatalf("off mapping: %v", got)
	}
	if got := thinkingBody("high"); str(got["effort"]) != "high" || got["mode"] != nil {
		t.Fatalf("high mapping: %v", got)
	}
	if got := thinkingBody(""); len(got) != 0 {
		t.Fatalf("default mapping: %v", got)
	}
	if thinkingLevelFrom("disabled", "") != "off" || thinkingLevelFrom("", "high") != "high" || thinkingLevelFrom("enabled", "") != "" || thinkingLevelFrom("", "") != "" {
		t.Fatal("level read-back mapping")
	}
}
