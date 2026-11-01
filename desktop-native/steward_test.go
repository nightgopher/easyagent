package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestStewardReusesConversationAndPreservesUnsentDraft(t *testing.T) {
	var creates atomic.Int32
	bodies := make(chan map[string]any, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/agents/steward":
			fmt.Fprint(w, `{"id":"agent-steward","name":"个人管家"}`)
		case r.URL.Path == "/sessions" && r.Method == "GET":
			if creates.Load() >= 2 {
				fmt.Fprint(w, `[{"id":"new-2","agent_id":"agent-steward","workspace":"/steward"}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case r.URL.Path == "/sessions" && r.Method == "POST":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodies <- body
			n := creates.Add(1)
			fmt.Fprintf(w, `{"id":"new-%d"}`, n)
		case strings.HasSuffix(r.URL.Path, "/info"):
			if strings.Contains(r.URL.Path, "new-2") {
				fmt.Fprint(w, `{"workspace":"/steward","agent_id":"agent-steward"}`)
			} else {
				fmt.Fprint(w, `{"workspace":"/workspace"}`)
			}
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.Live.Connected = true
	a.current().Draft = "给编程 Agent 的未发送需求"
	a.openSteward()
	waitNative(t, a, func() bool { return a.current().RemoteID == "new-2" && !a.Live.Loading["create-session"] })
	first, second := <-bodies, <-bodies
	if first["agent_id"] != nil || len(second) != 1 || second["agent_id"] != "agent-steward" {
		t.Fatalf("wrong identities: %#v %#v", first, second)
	}
	var original *session
	for _, s := range a.Sessions {
		if s.RemoteID == "new-1" {
			original = s
		}
	}
	if original == nil || original.Draft != "给编程 Agent 的未发送需求" || a.current().Draft != "" {
		t.Fatal("draft lost or carried into steward")
	}
	// Populate the server-owned identity as the real /info endpoint does.
	a.current().AgentID = "agent-steward"
	a.current().Draft = "管家自己的草稿"
	a.selectSession(original.ID)
	a.openSteward()
	waitNative(t, a, func() bool { return a.current().RemoteID == "new-2" && !a.Live.Loading["steward"] })
	if creates.Load() != 2 || a.current().Draft != "管家自己的草稿" || original.Draft == "" {
		t.Fatal("reopening did not reuse the steward conversation")
	}
}

func TestStewardLinksNavigateToArchivedSessionAndRejectInvalidURLs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/sessions" {
			fmt.Fprint(w, `[{"id":"original","title":"原报告","workspace":"/reports","archived":true,"agent_id":"agent-reports"}]`)
		} else {
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	a := newConnectedPreview(t.TempDir())
	defer a.closeLive()
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.Live.Connected = true
	origin := a.current()
	origin.RemoteID, origin.Draft = "steward-conversation", "保留问题"
	for _, href := range []string{"easyagent://session/../other", "easyagent://session/%2Fetc", "easyagent://session/original?prompt=execute", "easyagent://session/original?entry=a&entry=b", "easyagent://session/original?q=%00", "easyagent://remote-host/original", "easyagent://user@session/original", "easyagent://agent/name?tools=bash"} {
		if !a.openAgentLink(href) || a.Live.Error == "" || a.Live.Loading["navigate-session"] {
			t.Fatalf("accepted invalid navigation: %s", href)
		}
		a.Live.Error = ""
	}
	if a.openAgentLink("https://example.com") {
		t.Fatal("handled an external link as internal")
	}
	a.openAgentLink("easyagent://session/original?entry=old-message&q=report")
	waitNative(t, a, func() bool { return a.current().RemoteID == "original" })
	if !a.Archived || origin.Draft != "保留问题" || a.Live.SessionSearch.TargetQuery != "report" {
		t.Fatal("navigation lost archive, query or draft")
	}
	current := a.current()
	a.openAgentLink("easyagent://session/deleted")
	waitNative(t, a, func() bool { return !a.Live.Loading["navigate-session"] && a.Live.Error != "" })
	if a.current() != current {
		t.Fatal("missing session created a new conversation")
	}
	// Actual Markdown renderer routes the task link internally, without opening
	// an external URL or scheduling a model request.
	var document markdownDocument
	tt := ui.NewTester(func(c *ui.Context) {
		a.renderSessionMarkdown(c, &document, "[查看任务](easyagent://work)", a.current(), "", false)
	}, 600, 200)
	if document.options.OnLink == nil {
		t.Fatal("missing session link handler")
	}
	document.options.OnLink("easyagent://work")
	if !a.Live.Work.Open || len(tt.OpenedURLs()) != 0 {
		t.Fatal("internal link did not open the native inbox")
	}
}

func TestNativeCoreStewardEntryAndRevocation(t *testing.T) {
	a, _, _, calls := nativeCoreFixture(t)
	defer func() {
		if !t.Failed() {
			return
		}
		t.Logf("resource errors: %#v; active=%s agent=%s", a.Live.ResourceErrors, a.current().RemoteID, a.current().AgentID)
	}()
	a.current().Draft = "保留原编程草稿"
	a.openSteward()
	waitNative(t, a, func() bool {
		return a.current().AgentID == "agent-steward" && a.current().RemoteID != "" && a.current().Workspace != ""
	})
	id := a.current().RemoteID
	a.current().Draft = "管家草稿"
	a.openSteward()
	waitNative(t, a, func() bool {
		return !a.Live.Loading["steward"] && !a.Live.Loading["agent-session"] && !a.Live.Loading["create-session"]
	})
	if a.current().RemoteID != id || a.current().Draft != "管家草稿" || calls.Load() != 0 {
		t.Fatal("entry did not reuse state or sent a prompt")
	}
	var original *session
	for _, s := range a.Sessions {
		if s.Draft == "保留原编程草稿" {
			original = s
		}
	}
	if original == nil {
		t.Fatal("original draft disappeared")
	}
	if err := a.Live.Client.request(a.Live.hostCtx, "POST", "/work", map[string]any{"session_id": original.RemoteID, "title": "演示材料"}, nil); err != nil {
		t.Fatal(err)
	}
	a.current().Draft = "fixture_host_work"
	a.submitLive()
	waitNative(t, a, func() bool { return a.current().Run != nil && !a.current().Running && !a.current().Sending })
	found := false
	for _, m := range a.current().Messages {
		if m.Tool == "host_work_search" && strings.Contains(m.Result, "演示材料") && strings.Contains(m.Result, "easyagent://session/"+original.RemoteID) {
			found = true
		}
	}
	if !found {
		t.Fatal("steward did not read the host work catalog through the model tool loop")
	}
	var definition nativeBusinessAgent
	if err := a.Live.Client.request(a.Live.hostCtx, "GET", "/agents/agent-steward", nil, &definition); err != nil {
		t.Fatal(err)
	}
	definition.Disabled = true
	if err := a.Live.Client.request(a.Live.hostCtx, "PUT", "/agents/agent-steward", definition, nil); err != nil {
		t.Fatal(err)
	}
	a.openSteward()
	waitNative(t, a, func() bool { return !a.Live.Loading["steward"] && a.Live.ResourceErrors["steward"] != "" })
	var reopened nativeBusinessAgent
	if err := a.Live.Client.request(a.Live.hostCtx, "GET", "/agents/agent-steward", nil, &reopened); err != nil {
		t.Fatal(err)
	}
	if !reopened.Disabled || reopened.Version != definition.Version+1 {
		t.Fatal("opening reset the disabled agent")
	}
}
