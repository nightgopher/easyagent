package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func localSkill(name, description, root, origin string, managed bool) map[string]any {
	return map[string]any{
		"name": name, "description": description, "dir_name": name, "root": root, "origin": origin,
		"path": root + "/" + name + "/SKILL.md", "managed": managed, "model_invocation": true, "enabled": true,
	}
}

func fixtureLocalSkillsPreview(t *testing.T) *preview {
	t.Helper()
	a := alignmentPreview(t)
	agents, claude := "/home/q/.agents/skills", "/home/q/.claude/skills"
	shadowed := localSkill("hand-written", "旧版本，被覆盖", claude, "claude", false)
	shadowed["shadowed_by"] = agents + "/hand-written/SKILL.md"
	explicit := localSkill("explicit-only", "只能显式调用", claude, "claude", false)
	explicit["model_invocation"] = false
	a.Live.Data[localSkillsKey] = map[string]any{
		"skills": []any{
			localSkill("from-market", "从市场安装的技能", agents, "agents", true),
			localSkill("hand-written", "手写的技能", agents, "agents", false),
			shadowed,
			explicit,
			localSkill("other-tool", "其他工具装的技能", claude, "claude", false),
		},
		"roots":    []any{map[string]any{"path": agents, "origin": "agents"}, map[string]any{"path": claude, "origin": "claude"}},
		"problems": []any{map[string]any{"path": claude + "/other-tool/SKILL.md", "message": "description is empty"}},
	}
	a.Settings.Open = true
	a.Settings.Tab = "capabilities"
	a.Settings.CapabilityTab = "mine"
	return a
}

// hasExactText matches a whole rendered label; HasText also matches substrings of longer text.
func hasExactText(tt *ui.Tester, text string) bool {
	for _, got := range tt.Texts() {
		if got == text {
			return true
		}
	}
	return false
}

func TestMySkillsListsEverySkillAndOnlyOffersUninstallForMarketInstalls(t *testing.T) {
	for _, width := range []int{1240, 640} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			a := fixtureLocalSkillsPreview(t)
			tt := ui.NewTester(a.View, width, 1400)
			if !hasExactText(tt, "市场安装") {
				t.Fatal("the market-installed skill must carry the 市场安装 badge")
			}
			for _, want := range []string{"from-market", "hand-written", "other-tool", "手写的技能", "其他工具装的技能", "仅显式调用", "1 条加载提示"} {
				if !tt.HasText(want) {
					t.Fatalf("%d: missing %q in %v", width, want, tt.Texts())
				}
			}
			if !tt.HasText("已被覆盖") {
				t.Fatal("a skill shadowed by an earlier directory must say so")
			}
			if tt.HasText("卸载") || hasExactText(tt, "/home/q/.agents/skills/from-market/SKILL.md") {
				t.Fatal("details must stay collapsed in the overview")
			}
			if dir := os.Getenv("EASYAGENT_PREVIEW_SNAPSHOTS"); dir != "" {
				saveSnapshot(t, tt, filepath.Join(dir, fmt.Sprintf("my-skills-%d.png", width)))
				preview := ui.NewTester(a.View, width, 960)
				saveSnapshot(t, preview, filepath.Join(dir, fmt.Sprintf("my-skills-preview-%d.png", width)))
			}
			for _, rowName := range []string{"技能：from-market", "技能：hand-written", "技能：other-tool"} {
				row, ok := tt.Find(rowName)
				if !ok || row.X < 0 || row.X+row.W > float32(width) {
					t.Fatalf("%s overflows at width %d: %+v", rowName, width, row)
				}
			}
		})
	}
}

func TestMySkillsNeverOffersUninstallForSkillsTheMarketDidNotInstall(t *testing.T) {
	a := fixtureLocalSkillsPreview(t)
	skills := list(object(a.Live.Data[localSkillsKey])["skills"])
	for _, raw := range skills {
		object(raw)["managed"] = false
	}
	tt := ui.NewTester(a.View, 1240, 1400)
	if tt.HasText("卸载") || hasExactText(tt, "市场安装") {
		t.Fatalf("hand-written and other tools' skills must not be removable here: %v", tt.Texts())
	}
	object(skills[0])["managed"] = true
	tt.Frame()
	click(t, tt, "技能详情："+str(object(skills[0])["path"]))
	if !tt.HasText("卸载") || !hasExactText(tt, "市场安装") {
		t.Fatal("the market-installed skill lost its uninstall button")
	}
}

func TestMySkillsShowsTwoTabsAndNoSessionTab(t *testing.T) {
	a := fixtureLocalSkillsPreview(t)
	tt := ui.NewTester(a.View, 1240, 1000)
	if !tt.HasText("我的技能") || !tt.HasText("市场") {
		t.Fatalf("expected the 我的技能 and 市场 tabs, got %v", tt.Texts())
	}
	for _, gone := range []string{"当前会话已加载", "已安装技能"} {
		if tt.HasText(gone) {
			t.Fatalf("%q should no longer be on the skills page", gone)
		}
	}
}

func TestMySkillsSearchFiltersByNameAndDescription(t *testing.T) {
	a := fixtureLocalSkillsPreview(t)
	a.Settings.LocalSkillQuery = "其他工具"
	tt := ui.NewTester(a.View, 1240, 1000)
	if !tt.HasText("other-tool") || tt.HasText("from-market") || tt.HasText("手写的技能") {
		t.Fatalf("search by description did not filter: %v", tt.Texts())
	}
	a.Settings.LocalSkillQuery = "没有这个技能"
	tt.Frame()
	if !tt.HasText("没有匹配的技能。") {
		t.Fatal("an empty search result must be explained")
	}
}

func TestMySkillsEmptyHostPointsToTheMarket(t *testing.T) {
	a := fixtureLocalSkillsPreview(t)
	a.Live.Data[localSkillsKey] = map[string]any{"skills": []any{}, "roots": []any{}, "problems": []any{}}
	tt := ui.NewTester(a.View, 1240, 1000)
	if !tt.HasText("去技能市场安装") {
		t.Fatal("an empty list should offer the market")
	}
}

func TestMySkillsUninstallConfirmsThenReloadsBothLists(t *testing.T) {
	var deletes, localReads, installedReads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE" && r.URL.Path == "/skills/market/installed/from-market":
			deletes.Add(1)
			fmt.Fprint(w, `{}`)
		case r.URL.Path == "/skills/local":
			localReads.Add(1)
			if deletes.Load() > 0 {
				fmt.Fprint(w, `{"skills":[],"roots":[],"problems":[]}`)
			} else {
				fmt.Fprint(w, `{"skills":[{"name":"from-market","description":"d","dir_name":"from-market","origin":"agents","path":"/h/.agents/skills/from-market/SKILL.md","managed":true,"model_invocation":true}],"roots":[],"problems":[]}`)
			}
		case r.URL.Path == "/skills/market/installed":
			installedReads.Add(1)
			fmt.Fprint(w, `{"installed":[]}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	a := fixtureLocalSkillsPreview(t)
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.loadLocalSkills()
	waitNative(t, a, func() bool { return len(list(object(a.Live.Data[localSkillsKey])["skills"])) == 1 })
	tt := ui.NewTester(a.View, 1240, 1000)

	click(t, tt, "技能详情：/h/.agents/skills/from-market/SKILL.md")
	click(t, tt, "卸载")
	if deletes.Load() != 0 || a.marketState().ConfirmName != "from-market" {
		t.Fatal("uninstall skipped its confirmation")
	}
	click(t, tt, "取消")
	if deletes.Load() != 0 || a.marketState().ConfirmName != "" {
		t.Fatal("cancel did not abort the uninstall")
	}
	click(t, tt, "卸载")
	click(t, tt, "确认卸载")
	waitNative(t, a, func() bool {
		return deletes.Load() == 1 && installedReads.Load() >= 1 && len(list(object(a.Live.Data[localSkillsKey])["skills"])) == 0
	})
	if localReads.Load() < 2 {
		t.Fatalf("the skills list must be reloaded after an uninstall, reads=%d", localReads.Load())
	}
	tt.Frame()
	if tt.HasText("from-market") {
		t.Fatal("the uninstalled skill is still listed")
	}
}

func TestMySkillsOpenFolderShowsTheSkillsFolderLocally(t *testing.T) {
	a := fixtureLocalSkillsPreview(t)
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "solo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	solo := localSkill("solo", "唯一的技能", dir, "custom", false)
	solo["path"] = filepath.ToSlash(file)
	a.Live.Data[localSkillsKey] = map[string]any{"skills": []any{solo}, "roots": []any{}, "problems": []any{}}
	opened := make(chan string, 1)
	openLocalDirectory = func(p string) error { opened <- p; return nil }
	t.Cleanup(func() { openLocalDirectory = func(p string) error { return mygo.Shell.OpenPath(p) } })
	tt := ui.NewTester(a.View, 1240, 1000)

	click(t, tt, "技能详情："+filepath.ToSlash(file))
	click(t, tt, "打开文件夹")
	select {
	case got := <-opened:
		if got != skillDir {
			t.Fatalf("opened %q, want the skill's folder %q", got, skillDir)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the skill's folder was not opened")
	}

	a.Live.Config.Hosts = []hostConfig{{ID: "mini", Name: "迷你主机", URL: "http://192.168.5.16:8080"}}
	a.Live.Config.ActiveHost = "mini"
	tt.Frame()
	if tt.HasText("打开文件夹") {
		t.Fatal("a remote host's folders are not on this machine, so the action must not be offered")
	}
}

func TestOpeningTheSkillsPageLoadsTheHostSkills(t *testing.T) {
	var local atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/skills/local" {
			local.Add(1)
			fmt.Fprint(w, `{"skills":[{"name":"found","description":"d","dir_name":"found","origin":"agents","path":"/h/.agents/skills/found/SKILL.md","model_invocation":true}],"roots":[],"problems":[]}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	a := alignmentPreview(t)
	a.Live.Client, _ = newBackendClient(srv.URL, "")
	a.openSettings("capabilities")
	waitNative(t, a, func() bool { return len(list(object(a.Live.Data[localSkillsKey])["skills"])) == 1 })
	if local.Load() == 0 {
		t.Fatal("the skills page never asked the host for its skills")
	}
	if a.Settings.CapabilityTab != "mine" {
		t.Fatalf("the page should open on 我的技能, got %q", a.Settings.CapabilityTab)
	}
}

func TestMySkillsSaysWhenMarketRecordsCouldNotBeRead(t *testing.T) {
	a := fixtureLocalSkillsPreview(t)
	tt := ui.NewTester(a.View, 1240, 1400)
	if tt.HasText("无法读取市场安装记录") {
		t.Fatal("no warning expected when the records were read")
	}
	object(a.Live.Data[localSkillsKey])["installed_error"] = "permission denied"
	tt.Frame()
	if !tt.HasText("无法读取市场安装记录，下面的技能无法区分是否由市场安装，也暂时不能在这里卸载：permission denied") {
		t.Fatalf("a failed read of the install records must be visible: %v", tt.Texts())
	}
}

func TestReconnectDropsHostScopedSkillData(t *testing.T) {
	a := hostCredentialPreview(t, &memorySecrets{})
	a.Live.Config.ActiveHost = "remote"
	a.syncSettingsConfig()
	for _, key := range []string{"market", "installed", "sections", "skill-detail", localSkillsKey, "capabilities"} {
		a.Live.Data[key] = map[string]any{"from": "previous host"}
		a.Live.ResourceErrors[key] = "previous host failed"
	}
	a.connectLive()
	waitNative(t, a, func() bool { return !a.Live.Connecting })
	for _, key := range []string{"market", "installed", "sections", "skill-detail", localSkillsKey, "capabilities"} {
		if _, kept := a.Live.Data[key]; kept {
			t.Errorf("%s survived a host reconnect and would show the previous host's data", key)
		}
		if a.Live.ResourceErrors[key] != "" {
			t.Errorf("%s kept the previous host's error", key)
		}
	}
}

func TestSkillSwitchPersistsHostPreferenceWithoutChangingDraft(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			a := fixtureLocalSkillsPreview(t)
			a.current().Draft = "保留草稿"
			data := object(a.Live.Data[localSkillsKey])
			path := str(object(list(data["skills"])[0])["path"])
			requests := make(chan bool, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Path    string `json:"path"`
					Enabled *bool  `json:"enabled"`
				}
				err := json.NewDecoder(r.Body).Decode(&body)
				requests <- err == nil && r.Method == "PUT" && r.URL.Path == "/skills/local" && body.Path == path && body.Enabled != nil && !*body.Enabled
				if fail {
					http.Error(w, "save failed", 500)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"skills": []any{map[string]any{"name": "from-market", "path": path, "enabled": false, "model_invocation": true}}})
			}))
			defer srv.Close()
			a.Live.Client, _ = newBackendClient(srv.URL, "")
			tt := ui.NewTester(a.View, 1240, 1000)
			if tt.HasText("使用") {
				t.Fatal("settings must not offer the composer action")
			}
			click(t, tt, "启用技能："+path)
			select {
			case ok := <-requests:
				if !ok {
					t.Fatal("invalid toggle request")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no toggle request")
			}
			waitNative(t, a, func() bool { return !a.Live.Loading["skill-toggle"] })
			got := boolean(object(list(object(a.Live.Data[localSkillsKey])["skills"])[0])["enabled"])
			if got != fail {
				t.Fatalf("enabled=%v, failure=%v", got, fail)
			}
			if a.current().Draft != "保留草稿" || !a.Settings.Open {
				t.Fatal("toggle changed conversation or closed settings")
			}
			if fail && a.Live.ResourceErrors["skill-toggle"] == "" {
				t.Fatal("save failure must be visible")
			}
		})
	}
}
