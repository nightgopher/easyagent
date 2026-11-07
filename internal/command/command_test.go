package command

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func TestStoreGoalLifecycle(t *testing.T) {
	s := newTestStore(t)
	sp, err := s.CreateSpace("easyagent", "")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	g, err := s.CreateGoal(sp.ID, "跑通冒烟链路", "验证开工包→结果", []string{"分支出现", "提交匹配", " "})
	if err != nil {
		t.Fatalf("CreateGoal: %v", err)
	}
	if g.Number != 1 {
		t.Fatalf("first goal number = %d, want 1", g.Number)
	}
	if len(g.Done) != 2 {
		t.Fatalf("blank done lines should be dropped; got %d", len(g.Done))
	}
	if err := s.SetDoneItem(1, 0, true); err != nil {
		t.Fatalf("SetDoneItem: %v", err)
	}
	got, err := s.Goal(1)
	if err != nil || !got.Done[0].Check || got.Done[1].Check {
		t.Fatalf("done flags wrong: %+v err=%v", got.Done, err)
	}
	if err := s.SetGoalStatus(1, GoalClosed); err != nil {
		t.Fatalf("SetGoalStatus: %v", err)
	}
	// Persistence round-trip.
	s2 := NewStore(filepath.Dir(s.path()))
	g2, err := s2.Goal(1)
	if err != nil || g2.Status != GoalClosed || !g2.Done[0].Check {
		t.Fatalf("round-trip lost state: %+v err=%v", g2, err)
	}
}

func TestStoreDuplicateSpaceRejected(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateSpace("a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSpace("a", ""); err == nil {
		t.Fatal("duplicate space should fail")
	}
}

func TestAssignResultAndInbox(t *testing.T) {
	s := newTestStore(t)
	sp, _ := s.CreateSpace("sp", "")
	g, _ := s.CreateGoal(sp.ID, "goal", "", nil)
	res, err := s.AddManualResult(sp.ID, 0, "goal/9-orphan", "unassigned")
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.AddInboxItem(InboxItem{Kind: InboxUnassigned, Title: "未归属的结果：goal/9-orphan", SpaceID: sp.ID, ResultID: res.ID})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := s.AssignInboxResult(it.ID, g.Number)
	if err != nil {
		t.Fatalf("AssignInboxResult: %v", err)
	}
	if assigned != g.Number {
		t.Fatalf("assigned = %d, want %d", assigned, g.Number)
	}
	results := s.Results(sp.ID, g.Number)
	if len(results) != 1 || results[0].GoalNumber != g.Number {
		t.Fatalf("result not attached: %+v", results)
	}
	if items := s.Inbox(false); len(items) != 0 {
		t.Fatalf("inbox should be empty after assign, got %d", len(items))
	}
}

func TestAssignInboxCreatesGoal(t *testing.T) {
	s := newTestStore(t)
	sp, _ := s.CreateSpace("sp", "")
	res, _ := s.AddManualResult(sp.ID, 0, "goal/7-x", "")
	it, _ := s.AddInboxItem(InboxItem{Kind: InboxUnassigned, Title: "新方向", SpaceID: sp.ID, ResultID: res.ID})
	n, err := s.AssignInboxResult(it.ID, 0)
	if err != nil {
		t.Fatalf("assign-with-create: %v", err)
	}
	g, err := s.Goal(n)
	if err != nil || g.Title != "新方向" {
		t.Fatalf("created goal wrong: %+v err=%v", g, err)
	}
}

func TestKickoffContainsConventions(t *testing.T) {
	s := newTestStore(t)
	sp, err := s.CreateSpace("easyagent", "/tmp/some-repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSpaceNotes(sp.ID, "注意保持测试绿"); err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateGoal(sp.ID, "冒烟", "desc", []string{"出现分支"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.AddManualResult(sp.ID, g.Number, "goal/1-first", "first")
	text, err := s.Kickoff(g.Number)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# 开工包：目标 #1 冒烟",
		"- [ ] 出现分支",
		"/tmp/some-repo",
		"goal/1-<短横线描述>",
		"goal/1: <说明>",
		"Closes #1",
		"goal/1-first",
		"注意保持测试绿",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("kickoff missing %q\n---\n%s", want, text)
		}
	}
}

// ── discovery over a real git repo ────────────────────────────────────────

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "init")
	return dir
}

func commitOn(t *testing.T, dir, branch, parent, msg string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if parent == "" {
		run("checkout", "-q", "-b", branch)
	} else {
		run("checkout", "-q", "-b", branch, parent)
	}
	f := filepath.Join(dir, strings.NewReplacer("/", "_").Replace(branch)+".txt")
	if err := os.WriteFile(f, []byte(branch), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", msg)
	run("checkout", "-q", "main")
}

func TestScanAllRealGit(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	dir := initRepo(t)
	commitOn(t, dir, "goal/1-smoke", "", "goal/1: smoke run")
	commitOn(t, dir, "goal/42-orphan", "", "wip")
	commitOn(t, dir, "feature/other", "", "Closes #1 in a non-goal branch")                      // 非 goal 分支上的 Closes 也应被 commit 扫描捕获
	commitOn(t, dir, "plain", "", "goal/2: no such goal branch prefix? not a branch convention") // 普通分支不产生结果

	s := newTestStore(t)
	sp, err := s.CreateSpace("repo", dir)
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateGoal(sp.ID, "冒烟目标", "", []string{"分支"})
	if err != nil {
		t.Fatal(err)
	}

	report := s.ScanAll(context.Background(), ExecGit)
	if report.NewResults != 2 { // goal/1-smoke + goal/42-orphan（分支）；commit 结果另计
		t.Fatalf("branch results = %d, want 2; errs=%v", report.NewResults, report.SpaceErrs)
	}
	if report.NewInbox != 2 { // #1 新结果 + 未归属 goal/42
		t.Fatalf("inbox = %d, want 2", report.NewInbox)
	}
	// goal/1-smoke → attached to goal 1
	results := s.Results(sp.ID, g.Number)
	if len(results) < 1 || results[0].Ref != "goal/1-smoke" {
		t.Fatalf("goal results: %+v", results)
	}
	// goal/42-orphan → unassigned
	un := s.Results(sp.ID, 0)
	if len(un) != 1 || un[0].Ref != "goal/42-orphan" {
		t.Fatalf("unassigned: %+v", un)
	}

	// Idempotent: second scan finds nothing new.
	report2 := s.ScanAll(context.Background(), ExecGit)
	if report2.NewResults != 0 || report2.NewInbox != 0 {
		t.Fatalf("rescan not idempotent: %+v", report2)
	}
}

func TestScanAllBadRepoReportsError(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateSpace("broken", "/nonexistent/repo"); err != nil {
		t.Fatal(err)
	}
	report := s.ScanAll(context.Background(), ExecGit)
	if len(report.SpaceErrs) == 0 {
		t.Fatalf("expected space error for missing repo, got %+v", report)
	}
	if report.NewResults != 0 {
		t.Fatalf("no results expected, got %d", report.NewResults)
	}
}
