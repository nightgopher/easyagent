package submit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/operations"
)

// newCheckout builds a sandbox: a bare remote and a clone of it on main with
// one content file and one protected file. Nothing here reaches a real host.
func newCheckout(t *testing.T) (work, remote string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	work = filepath.Join(root, "work")
	git(t, root, "init", "-q", "--bare", "-b", "main", remote)
	git(t, root, "init", "-q", "-b", "main", work)
	for setting, value := range map[string]string{
		"user.name": "Site Bot", "user.email": "bot@example.com", "commit.gpgsign": "false",
		"core.hooksPath": filepath.Join(root, "hooks"),
	} {
		git(t, work, "config", setting, value)
	}
	write(t, work, "src/content/news/a.md", "a\n")
	write(t, work, "astro.config.mjs", "export default {}\n")
	git(t, work, "add", "-A")
	git(t, work, "commit", "-q", "-m", "init")
	git(t, work, "remote", "add", "origin", remote)
	git(t, work, "push", "-q", "-u", "origin", "main")
	return work, remote
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	out, err := operations.LocalBashOperations{}.Run(t.Context(), operations.RunRequest{
		Command: "git " + strings.Join(quoted, " "), WorkDir: dir, Env: []string{"GIT_TERMINAL_PROMPT=0"},
	})
	require.NoError(t, err, "git %s", strings.Join(args, " "))
	require.Equal(t, 0, out.ExitCode, "git %s: %s", strings.Join(args, " "), out.Output)
	return string(out.Output)
}

func write(t *testing.T, work, rel, content string) {
	t.Helper()
	path := filepath.Join(work, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// newTool is a deployment that commits content after its checks passed.
func newTool(t *testing.T, work string, gate ...string) *Tool {
	t.Helper()
	return NewTool(config.SubmitConfig{
		Gate: gate, PreviewURL: "http://test.example.com/", NextSteps: "由负责人验收。",
	}, []string{"src/content/**"}, work, operations.LocalBashOperations{})
}

func call(t *testing.T, tool *Tool, args string) (*receipt, error) {
	t.Helper()
	validated, err := tool.Validate(json.RawMessage(args))
	require.NoError(t, err)
	result, err := tool.Execute(t.Context(), validated, nil)
	if err != nil {
		assert.True(t, result.IsError)
		return nil, err
	}
	details, ok := result.Details.(map[string]any)
	require.True(t, ok, "a submit result carries details")
	assert.Equal(t, DetailsKind, details["kind"])
	return details["receipt"].(*receipt), nil
}

func remoteHeads(t *testing.T, remote string) map[string]string {
	t.Helper()
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(git(t, t.TempDir(), "ls-remote", "--heads", remote)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			heads[strings.TrimPrefix(fields[1], "refs/heads/")] = fields[0]
		}
	}
	return heads
}

// advanceRemote makes the remote's main move, as a colleague's merge would.
func advanceRemote(t *testing.T, remote string) {
	t.Helper()
	dir, other := t.TempDir(), filepath.Join(t.TempDir(), "other")
	git(t, dir, "clone", "-q", remote, other)
	for setting, value := range map[string]string{
		"user.name": "Colleague", "user.email": "colleague@example.com", "commit.gpgsign": "false",
	} {
		git(t, other, "config", setting, value)
	}
	write(t, other, "colleague.md", "merged\n")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-q", "-m", "Colleague's merge")
	git(t, other, "push", "-q", "origin", "main")
}

func TestSubmitCommitsAllowedChangesToANewTaskBranch(t *testing.T) {
	work, remote := newCheckout(t)
	before := remoteHeads(t, remote)
	write(t, work, "src/content/news/a.md", "changed\n")
	write(t, work, "src/content/news/b.md", "new\n")

	r, err := call(t, newTool(t, work, "true"), `{"summary":"Add news b","preview_paths":["/news/b/"]}`)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(r.Branch, "bot/add-news-b-"), r.Branch)
	assert.True(t, r.Pushed)
	assert.NotEmpty(t, r.Commit)
	assert.Equal(t, []fileChange{{"M", "src/content/news/a.md"}, {"A", "src/content/news/b.md"}}, r.Files)
	require.Len(t, r.Gate, 1)
	assert.True(t, r.Gate[0].OK)
	assert.Equal(t, []string{"http://test.example.com/news/b/"}, r.Preview)

	// Only the task branch reached the remote; main is untouched on both sides.
	heads := remoteHeads(t, remote)
	assert.Equal(t, before["main"], heads["main"])
	assert.Len(t, heads, 2)
	assert.NotEmpty(t, heads[r.Branch])
	assert.Equal(t, r.Branch, strings.TrimSpace(git(t, work, "rev-parse", "--abbrev-ref", "HEAD")))
	assert.Empty(t, git(t, work, "status", "--porcelain"))

	text := r.markdown()
	for _, want := range []string{
		"### 已提交：Add news b", "**分支** `" + r.Branch + "`", "**提交** `" + r.Commit + "`", "已推送到远端",
		"修改 `src/content/news/a.md`", "新增 `src/content/news/b.md`", "**质检结果**", "✔ true",
		"**测试环境预览**", "http://test.example.com/news/b/", "**下一步**：由负责人验收。",
	} {
		assert.Contains(t, text, want)
	}
}

func TestSubmitStagesWhatTheGateLeavesBehind(t *testing.T) {
	work, _ := newCheckout(t)
	write(t, work, "src/content/news/a.md", "changed\n")
	// The gate writes a file of its own; the commit must show the tree after
	// the gate, not the tree before it.
	tool := newTool(t, work, "printf 'generated\\n' > src/content/news/generated.md")

	r, err := call(t, tool, `{"summary":"Run the generator"}`)
	require.NoError(t, err)
	assert.Equal(t, []fileChange{{"M", "src/content/news/a.md"}, {"A", "src/content/news/generated.md"}}, r.Files)
	assert.Empty(t, git(t, work, "status", "--porcelain"))
}

func TestSubmitRefusesChangesOutsideTheIncludedPaths(t *testing.T) {
	cases := map[string]func(t *testing.T, work string){
		"a protected file":   func(t *testing.T, work string) { write(t, work, "astro.config.mjs", "changed\n") },
		"a new file at root": func(t *testing.T, work string) { write(t, work, "evil.txt", "x") },
		"a file moved out": func(t *testing.T, work string) {
			require.NoError(t, os.Rename(filepath.Join(work, "src/content/news/a.md"), filepath.Join(work, "moved.md")))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			work, remote := newCheckout(t)
			change(t, work)
			_, err := call(t, newTool(t, work, "true"), `{"summary":"Should not land"}`)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "已拦截")
			assert.Contains(t, err.Error(), "src/content/**")
			// Nothing was committed, branched or pushed, and the edit is still there to undo.
			assert.Equal(t, "main", strings.TrimSpace(git(t, work, "rev-parse", "--abbrev-ref", "HEAD")))
			assert.NotEmpty(t, git(t, work, "status", "--porcelain"))
			assert.Empty(t, git(t, work, "branch", "--list", "bot/*"))
			assert.Equal(t, []string{"main"}, headNames(remoteHeads(t, remote)))
		})
	}
}

func headNames(heads map[string]string) []string {
	names := make([]string, 0, len(heads))
	for name := range heads {
		names = append(names, name)
	}
	return names
}

func TestSubmitStopsAtTheFirstFailingCheck(t *testing.T) {
	work, remote := newCheckout(t)
	write(t, work, "src/content/news/a.md", "changed\n")
	tool := newTool(t, work, "echo first", "echo problem-here; exit 3", "echo never-ran")

	_, err := call(t, tool, `{"summary":"Broken content"}`)
	require.Error(t, err)
	for _, want := range []string{"质检未通过", "echo problem-here; exit 3", "problem-here"} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "never-ran")
	assert.Equal(t, []string{"main"}, headNames(remoteHeads(t, remote)))
	assert.Empty(t, git(t, work, "branch", "--list", "bot/*"))
}

func TestSubmitRefusesWhenThereIsNothingToCommit(t *testing.T) {
	work, _ := newCheckout(t)
	_, err := call(t, newTool(t, work, "true"), `{"summary":"Nothing"}`)
	assert.ErrorContains(t, err, "没有可提交的改动")
}

func TestSubmitAddsAFollowUpToTheSameTaskBranch(t *testing.T) {
	work, remote := newCheckout(t)
	tool := newTool(t, work, "true")
	write(t, work, "src/content/news/a.md", "one\n")
	first, err := call(t, tool, `{"summary":"First change"}`)
	require.NoError(t, err)

	write(t, work, "src/content/news/a.md", "two\n")
	second, err := call(t, tool, `{"summary":"Tweak the title"}`)
	require.NoError(t, err)
	assert.Equal(t, first.Branch, second.Branch)
	assert.NotEqual(t, first.Commit, second.Commit)
	assert.Equal(t, []fileChange{{"M", "src/content/news/a.md"}}, second.Files)
	// The remote task branch has both commits; main has neither.
	assert.Equal(t, "Tweak the title\nFirst change", strings.TrimSpace(git(t, work, "log", "--format=%s", "main.."+first.Branch)))
	assert.Len(t, remoteHeads(t, remote), 2)
}

func TestSubmitWarnsWhenTheBaseIsBehind(t *testing.T) {
	work, remote := newCheckout(t)
	advanceRemote(t, remote)
	write(t, work, "src/content/news/a.md", "changed\n")

	r, err := call(t, newTool(t, work, "true"), `{"summary":"News"}`)
	require.NoError(t, err)
	require.Len(t, r.Warnings, 1)
	assert.Contains(t, r.Warnings[0], "落后远端 1 个提交")
	assert.Contains(t, r.markdown(), "⚠️")
}

func TestNewTaskReturnsACleanWorkspaceToAnUpToDateBase(t *testing.T) {
	work, remote := newCheckout(t)
	tool := newTool(t, work, "true")
	write(t, work, "src/content/news/a.md", "one\n")
	first, err := call(t, tool, `{"summary":"First task"}`)
	require.NoError(t, err)

	write(t, work, "src/content/news/a.md", "uncommitted\n")
	_, err = call(t, tool, `{"summary":"","new_task":true}`)
	assert.ErrorContains(t, err, "必须没有未提交的改动")
	assert.Equal(t, first.Branch, strings.TrimSpace(git(t, work, "rev-parse", "--abbrev-ref", "HEAD")))

	write(t, work, "src/content/news/a.md", "one\n")
	advanceRemote(t, remote)
	ready, err := call(t, tool, `{"summary":"","new_task":true}`)
	require.NoError(t, err)
	assert.True(t, ready.Ready)
	assert.Equal(t, "main", ready.Branch)
	assert.Empty(t, ready.Warnings)
	assert.Contains(t, ready.markdown(), "可以开始新任务")

	// The local main caught up with the remote, and the next change starts its own task.
	assert.Equal(t, remoteHeads(t, remote)["main"], strings.TrimSpace(git(t, work, "rev-parse", "main")))
	assert.FileExists(t, filepath.Join(work, "colleague.md"))
	write(t, work, "src/content/news/a.md", "two\n")
	second, err := call(t, tool, `{"summary":"Second task"}`)
	require.NoError(t, err)
	assert.NotEqual(t, first.Branch, second.Branch)
	assert.Empty(t, second.Warnings)
}

func TestSubmitRetriesAPushThatFailedBefore(t *testing.T) {
	work, remote := newCheckout(t)
	write(t, work, "src/content/news/a.md", "changed\n")
	// An unreachable remote makes the first push fail after the commit exists.
	git(t, work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	tool := newTool(t, work, "true")

	_, err := call(t, tool, `{"summary":"News"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "但没有推送")
	assert.Contains(t, git(t, work, "branch", "--list", "bot/*"), "bot/")

	git(t, work, "remote", "set-url", "origin", remote)
	r, err := call(t, tool, `{"summary":"retry"}`)
	require.NoError(t, err)
	assert.True(t, r.Pushed)
	assert.Len(t, remoteHeads(t, remote), 2)

	// With nothing pending, the same call is refused instead of pushing again.
	_, err = call(t, tool, `{"summary":"again"}`)
	assert.ErrorContains(t, err, "没有可提交的改动")
}

func TestSubmitWithoutPushKeepsTheCommitLocal(t *testing.T) {
	work, remote := newCheckout(t)
	write(t, work, "src/content/news/a.md", "changed\n")
	push := false
	tool := NewTool(config.SubmitConfig{Gate: []string{"true"}, Push: &push}, []string{"src/content/**"}, work, operations.LocalBashOperations{})

	r, err := call(t, tool, `{"summary":"News"}`)
	require.NoError(t, err)
	assert.False(t, r.Pushed)
	assert.Contains(t, r.markdown(), "仅本地提交，未推送")
	assert.Equal(t, []string{"main"}, headNames(remoteHeads(t, remote)))
}

func TestSubmitCreatesAMergeRequest(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "secret-token", r.Header.Get("PRIVATE-TOKEN"))
		switch r.Method {
		case http.MethodPost:
			assert.Equal(t, "/api/v4/projects/group%2Fsite/merge_requests", r.URL.EscapedPath())
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "main", body["target_branch"])
			assert.Equal(t, "[bot] News", body["title"])
			if posts.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"web_url":"https://gitlab.example.com/group/site/-/merge_requests/1"}`))
				return
			}
			w.WriteHeader(http.StatusConflict)
		case http.MethodGet:
			assert.Equal(t, "/api/v4/projects/group%2Fsite/merge_requests", r.URL.EscapedPath())
			assert.Equal(t, "opened", r.URL.Query().Get("state"))
			assert.True(t, strings.HasPrefix(r.URL.Query().Get("source_branch"), "bot/"))
			_, _ = w.Write([]byte(`[{"web_url":"https://gitlab.example.com/group/site/-/merge_requests/1"}]`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	t.Setenv("SUBMIT_TOKEN", "secret-token")

	work, _ := newCheckout(t)
	tool := NewTool(config.SubmitConfig{
		Gate: []string{"true"},
		MR: &config.MergeRequestConfig{
			APIURL: server.URL + "/api/v4", Project: "group/site", TokenEnv: "SUBMIT_TOKEN", TitlePrefix: "[bot] ",
		},
	}, []string{"src/content/**"}, work, operations.LocalBashOperations{})
	write(t, work, "src/content/news/a.md", "one\n")
	first, err := call(t, tool, `{"summary":"News"}`)
	require.NoError(t, err)
	assert.Equal(t, "https://gitlab.example.com/group/site/-/merge_requests/1", first.MRURL)

	// The second push finds the merge request that is already open.
	write(t, work, "src/content/news/a.md", "two\n")
	second, err := call(t, tool, `{"summary":"News"}`)
	require.NoError(t, err)
	assert.Equal(t, first.MRURL, second.MRURL)
	assert.Empty(t, second.Warnings)
}

func TestSubmitReportsAMergeRequestThatCouldNotBeCreated(t *testing.T) {
	work, _ := newCheckout(t)
	tool := NewTool(config.SubmitConfig{
		Gate: []string{"true"},
		MR:   &config.MergeRequestConfig{APIURL: "http://127.0.0.1:1/api/v4", Project: "x", TokenEnv: "SUBMIT_TOKEN"},
	}, []string{"src/content/**"}, work, operations.LocalBashOperations{})
	t.Setenv("SUBMIT_TOKEN", "secret-token")
	write(t, work, "src/content/news/a.md", "one\n")

	// The push stands; only the merge request is missing.
	r, err := call(t, tool, `{"summary":"News"}`)
	require.NoError(t, err)
	assert.True(t, r.Pushed)
	require.Len(t, r.Warnings, 1)
	assert.Contains(t, r.Warnings[0], "合并请求创建失败")
}

func TestValidateRejectsUnusableArguments(t *testing.T) {
	tool := newTool(t, t.TempDir(), "true")
	for name, args := range map[string]string{
		"no summary":      `{"summary":"  "}`,
		"too long":        `{"summary":"` + strings.Repeat("长", 501) + `"}`,
		"absolute url":    `{"summary":"x","preview_paths":["https://evil.example/"]}`,
		"path with space": `{"summary":"x","preview_paths":["/a b"]}`,
		"too many paths":  `{"summary":"x","preview_paths":["/1","/2","/3","/4","/5","/6","/7","/8","/9","/10","/11"]}`,
		"not an object":   `["x"]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tool.Validate(json.RawMessage(args))
			assert.Error(t, err)
		})
	}
	// Starting a task needs no summary of a change.
	validated, err := tool.Validate(json.RawMessage(`{"summary":"  ","new_task":true}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"summary":"","new_task":true}`, string(validated))
}

func TestBranchNamesComeFromTheSummary(t *testing.T) {
	tool := newTool(t, t.TempDir(), "true")
	stamp := time.Date(2026, 11, 24, 10, 20, 30, 0, time.UTC)
	tool.now = func() time.Time { return stamp }
	for summary, want := range map[string]string{
		"Add news b":                 "bot/add-news-b-20261124-102030",
		"发一篇新闻":                      "bot/task-20261124-102030",
		"UPPER Case_and 99 numbers!": "bot/upper-case-and-99-numbers-20261124-102030",
	} {
		assert.Equal(t, want, tool.branchName(t.Context(), summary), summary)
	}
}

func TestSubmitsToOneWorkspaceShareALock(t *testing.T) {
	first := workspaceLock("/same")
	assert.Same(t, first, workspaceLock("/same"))
	assert.NotSame(t, first, workspaceLock("/other"))
}
