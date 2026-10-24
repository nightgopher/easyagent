package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

func projectSkill(name, description, origin string, overrides string, invocation bool) map[string]any {
	skill := map[string]any{
		"name": name, "description": description, "scope": "project", "origin": origin,
		"source": "/demo/easyagent/.agents/skills/" + name + "/SKILL.md", "model_invocation": invocation,
	}
	if origin == "claude" {
		skill["source"] = "/demo/easyagent/.claude/skills/" + name + "/SKILL.md"
	}
	if overrides != "" {
		skill["overrides"] = overrides
	}
	return skill
}

func fixtureSkillsWorkbench(t *testing.T) *preview {
	t.Helper()
	a := alignmentPreview(t)
	a.Live.Data["capabilities"] = map[string]any{
		"workspace": "/demo/easyagent",
		"skills": []any{
			projectSkill("deploy-notes", "整理部署记录", "agents", "/home/q/.agents/skills/deploy-notes/SKILL.md", true),
			projectSkill("claude-style", "兼容 .claude 目录的项目技能", "claude", "", false),
			map[string]any{"name": "find-skills", "description": "查找技能", "scope": "personal", "source": "/home/q/.agents/skills/find-skills/SKILL.md", "model_invocation": true},
			map[string]any{"name": "ego-browser", "description": "操作浏览器", "scope": "personal", "source": "/home/q/.agents/skills/ego-browser/SKILL.md", "model_invocation": true},
		},
	}
	a.Workbench = true
	a.Live.Workbench.Tab = "skills"
	return a
}

func TestSkillsWorkbenchShowsProjectSkillsAndSummarisesPersonalOnes(t *testing.T) {
	for _, width := range []int{1240, 900} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := fixtureSkillsWorkbench(t)
			tt := ui.NewTester(a.View, width, 900)
			for _, want := range []string{
				"本项目技能 · 2", "deploy-notes", "claude-style", "整理部署记录",
				".agents/skills/deploy-notes/SKILL.md", ".claude/skills/claude-style/SKILL.md",
				"仅显式调用", "本会话还加载了 2 个个人技能",
			} {
				if !tt.HasText(want) {
					t.Fatalf("%d: missing %q in %v", width, want, tt.Texts())
				}
			}
			if !hasExactText(tt, "覆盖个人同名技能") {
				t.Fatal("a project skill that hides a personal one must say so")
			}
			if tt.HasText("/demo/easyagent/.agents/skills/deploy-notes") {
				t.Fatal("paths should be shown relative to the project")
			}
			if tt.HasText("find-skills") || tt.HasText("ego-browser") {
				t.Fatal("personal skills are only summarised until expanded")
			}
			if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
				saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("project-skills-%d.png", width)))
			}
			click(t, tt, "本会话还加载了 2 个个人技能")
			if !tt.HasText("find-skills") || !tt.HasText("ego-browser") || !tt.HasText("管理个人技能") {
				t.Fatalf("expanding must list the personal skills: %v", tt.Texts())
			}
			if card, ok := tt.Find("项目技能：deploy-notes"); !ok || card.X+card.W > float32(width) {
				t.Fatalf("card overflows at width %d: %+v", width, card)
			}
		})
	}
}

func TestSkillsWorkbenchExplainsAnEmptyProject(t *testing.T) {
	a := fixtureSkillsWorkbench(t)
	a.Live.Data["capabilities"] = map[string]any{"workspace": "/demo/easyagent", "skills": []any{}}
	tt := ui.NewTester(a.View, 1240, 900)
	if !tt.HasText("本项目技能 · 0") || !tt.HasText("当前项目没有技能") || !tt.HasText(".agents/skills/<名称>/SKILL.md") {
		t.Fatalf("an empty project must say where skills go: %v", tt.Texts())
	}
	if tt.HasText("本会话还加载了") {
		t.Fatal("no personal-skills summary when there are none")
	}
}

func TestSkillsWorkbenchTabIsInTheIconColumnAndSlashCommand(t *testing.T) {
	a := fixtureSkillsWorkbench(t)
	a.Workbench = false
	a.Live.Workbench.Tab = "files"
	if !a.executeLiveCommand("/skills") {
		t.Fatal("/skills is not handled")
	}
	if !a.Workbench || a.Live.Workbench.Tab != "skills" {
		t.Fatalf("/skills must open the 技能 tab, got open=%v tab=%q", a.Workbench, a.Live.Workbench.Tab)
	}
	tt := ui.NewTester(a.View, 1240, 900)
	if _, ok := tt.Find("技能"); !ok {
		t.Fatalf("the 技能 tab is missing from the workspace panel's icon column: %v", tt.Texts())
	}
	found := false
	for _, cmd := range nativeCommands {
		found = found || cmd.Name == "/skills"
	}
	if !found {
		t.Fatal("/skills is not offered in the slash command menu")
	}
}

func TestSkillsWorkbenchManagePersonalSkillsOpensMySkills(t *testing.T) {
	a := fixtureSkillsWorkbench(t)
	tt := ui.NewTester(a.View, 1240, 900)
	click(t, tt, "本会话还加载了 2 个个人技能")
	click(t, tt, "管理个人技能")
	if !a.Settings.Open || a.Settings.Tab != "capabilities" || a.Settings.CapabilityTab != "mine" {
		t.Fatalf("expected the settings 我的技能 tab, got open=%v tab=%q cap=%q", a.Settings.Open, a.Settings.Tab, a.Settings.CapabilityTab)
	}
}

func TestSkillsWorkbenchRefreshRescansAndReloadsTheList(t *testing.T) {
	var refreshes, reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/sessions/layout/skills/refresh":
			refreshes.Add(1)
			fmt.Fprint(w, `{"skills":2}`)
		case r.Method == "GET" && r.URL.Path == "/sessions/layout/capabilities":
			reads.Add(1)
			if refreshes.Load() == 0 {
				fmt.Fprint(w, `{"workspace":"/demo/easyagent","skills":[]}`)
			} else {
				fmt.Fprint(w, `{"workspace":"/demo/easyagent","skills":[{"name":"just-added","description":"刷新后才出现","scope":"project","origin":"agents","source":"/demo/easyagent/.agents/skills/just-added/SKILL.md","model_invocation":true}]}`)
			}
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	a := fixtureSkillsWorkbench(t)
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.openResource("skills")
	waitNative(t, a, func() bool { return reads.Load() >= 1 && !a.Live.Loading["capabilities"] })
	tt := ui.NewTester(a.View, 1240, 900)
	if tt.HasText("just-added") {
		t.Fatal("the new skill should not be visible before a refresh")
	}

	click(t, tt, "刷新技能")
	waitNative(t, a, func() bool {
		return refreshes.Load() == 1 && !a.Live.Loading["skills-refresh"] && reads.Load() >= 2 && !a.Live.Loading["capabilities"]
	})
	tt.Frame()
	if !tt.HasText("just-added") || !tt.HasText("本项目技能 · 1") {
		t.Fatalf("the refreshed list was not shown: %v", tt.Texts())
	}
}

func TestSkillsWorkbenchRefreshWaitsForARunningTask(t *testing.T) {
	var refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			refreshes.Add(1)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	a := fixtureSkillsWorkbench(t)
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.current().Running = true
	tt := ui.NewTester(a.View, 1240, 900)
	_ = tt.Click("刷新技能")
	tt.Frame()
	if refreshes.Load() != 0 {
		t.Fatal("refreshing while a task runs would fail on the host; the button must be disabled")
	}
}

func TestSkillsWorkbenchHidesOpenFolderOnRemoteHosts(t *testing.T) {
	a := fixtureSkillsWorkbench(t)
	tt := ui.NewTester(a.View, 1240, 900)
	if !tt.HasText("打开文件夹") {
		t.Fatal("this machine's own projects can be opened in the file manager")
	}
	a.Live.Config.Hosts = []hostConfig{{ID: "mini", Name: "迷你主机", URL: "http://192.168.5.16:8080"}}
	a.Live.Config.ActiveHost = "mini"
	tt.Frame()
	if tt.HasText("打开文件夹") {
		t.Fatal("a remote host's folders are not on this machine")
	}
}
