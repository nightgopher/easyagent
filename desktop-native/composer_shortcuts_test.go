package main

import (
	"testing"
)

func TestComposerTriggersAreDistinct(t *testing.T) {
	for _, tc := range []struct{ text, file, expert string }{
		{"检查 #src", "src", ""}, {"找 @设计师", "", "设计师"}, {"#", "", ""},
	} {
		file, f := mentionQuery(tc.text)
		expert, e := expertQuery(tc.text)
		if tc.text == "#" {
			if !f || e {
				t.Fatal("# must open only files")
			}
			continue
		}
		if file != tc.file || expert != tc.expert || f == e {
			t.Fatalf("%q: file %q/%v, expert %q/%v", tc.text, file, f, expert, e)
		}
	}
	if _, ok := expertQuery("mail@example.com"); ok {
		t.Fatal("email must not trigger experts")
	}
}

func TestSkillSuggestionsIncludeEnabledAndRespectCommandNames(t *testing.T) {
	a := newConnectedPreview(t.TempDir())
	a.current().RemoteID = "session-1"
	a.Live.Data["composer-skills/session-1"] = map[string]any{"skills": []any{
		map[string]any{"name": "review", "description": "审查"},
		map[string]any{"name": "workflow", "description": "冲突"},
		map[string]any{"name": "disabled", "enabled": false},
	}}
	matches := a.commandMatches("/rev")
	if len(matches) != 1 || matches[0].Name != "/review" {
		t.Fatalf("unexpected suggestions: %#v", matches)
	}
	if len(a.commandMatches("/disabled")) != 0 {
		t.Fatal("disabled skill was suggested")
	}
	matches = a.commandMatches("/workflow")
	if len(matches) != 1 || matches[0].Description == "技能 · 冲突" {
		t.Fatalf("built-in command must win: %#v", matches)
	}
}

func TestHiddenProjectsSurviveRestartAndCanBeRestored(t *testing.T) {
	dir := t.TempDir()
	a := newConnectedPreview(dir)
	key := "local\x00/project"
	a.setProjectHidden(key, true)
	if !newConnectedPreview(dir).Live.HiddenProjects[key] {
		t.Fatal("hidden project did not persist")
	}
	a.setProjectHidden(key, false)
	if newConnectedPreview(dir).Live.HiddenProjects[key] {
		t.Fatal("restored project remained hidden")
	}
}

func TestExpertSelectionStaysInComposerAndPersistsWithDraft(t *testing.T) {
	dir := t.TempDir()
	a := newConnectedPreview(dir)
	a.Live.Connected = true
	s := a.current()
	s.RemoteID = "session-1"
	s.Draft = "请帮忙 @会议"
	a.Live.Data["business-agents"] = map[string]any{"agents": []any{
		map[string]any{"id": "expert-1", "name": "会议专家"},
		map[string]any{"id": "expert-2", "name": "会议助手", "disabled": true},
	}}
	a.completeExpert(s)
	if a.current() != s || s.Draft != "请帮忙 " || s.SelectedExpertID != "expert-1" {
		t.Fatalf("selection changed session or lost draft: %#v", s)
	}
	state := a.captureDraftInputs()
	version := a.Live.Store.reserve("draft-inputs.json")
	if err := a.Live.Store.save("draft-inputs.json", version, state); err != nil {
		t.Fatal(err)
	}
	b := newConnectedPreview(dir)
	b.current().RemoteID = "session-1"
	b.restoreDraftInputs(b.current())
	if b.current().SelectedExpertID != "expert-1" {
		t.Fatal("selected expert did not survive restart")
	}
}

func TestExpertSendCreatesSeparateAgentSession(t *testing.T) {
	a, requests := workspacePickerFixture(t)
	origin := a.current()
	origin.Draft = "整理记录"
	origin.SelectedExpertID, origin.SelectedExpertName = "expert-1", "会议专家"
	a.submitLive()
	request := <-requests
	if str(request["agent_id"]) != "expert-1" {
		t.Fatalf("wrong expert: %v", request)
	}
	waitNative(t, a, func() bool { return a.current() != origin })
	if a.current().AgentID != "expert-1" || a.current().Draft != "整理记录" {
		t.Fatalf("expert task was not preserved: %#v", a.current())
	}
	if origin.Draft != "" || origin.SelectedExpertID != "" {
		t.Fatal("source draft was not cleared after successful creation")
	}
}
