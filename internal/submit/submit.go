// Package submit provides submit_task, the one path from a session to a
// remote repository. The tool refuses changes outside the allowed paths,
// runs the deployment's gate, and only then commits to a task branch and
// pushes that branch. It never pushes the base branch and never forces.
package submit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/operations"
	"github.com/nightgopher/easyagent/sdk/tools"
)

// ToolName is the name of the submit tool.
const ToolName = "submit_task"

const (
	gitTimeout      = time.Minute
	pushTimeout     = 2 * time.Minute
	defaultRemote   = "origin"
	defaultBase     = "main"
	defaultNextStep = "如需微调请直接回复；验收通过后由负责人合并到主分支并发布。"
	maxSummary      = 500
)

// Tool is the submit_task tool bound to one workspace.
type Tool struct {
	cfg       config.SubmitConfig
	allow     tools.WritePolicy
	ops       operations.BashOperations
	client    *http.Client
	workspace string
	now       func() time.Time
}

// NewTool builds the tool for one workspace. include lists the paths that may
// be committed; an empty list allows every path. ops runs git and the gate.
func NewTool(cfg config.SubmitConfig, include []string, workspace string, ops operations.BashOperations) *Tool {
	return &Tool{
		cfg:       cfg,
		allow:     tools.WritePolicy{Allow: include},
		ops:       ops,
		client:    &http.Client{Timeout: 20 * time.Second},
		workspace: workspace,
		now:       time.Now,
	}
}

// Params are the arguments of submit_task.
type Params struct {
	// Summary is one sentence about the change; it becomes the commit title.
	Summary string `json:"summary"`
	// NewTask returns a clean workspace to the base branch, so the next change
	// starts a new task instead of joining the current one.
	NewTask bool `json:"new_task,omitempty"`
	// PreviewPaths are site paths to list under the preview URL.
	PreviewPaths []string `json:"preview_paths,omitempty"`
}

func (t *Tool) Name() string { return ToolName }

func (t *Tool) Description() string {
	return "Commit the current changes as a task and push them to a task branch. This is the only way to publish work: git is read-only in bash. " +
		"The tool first refuses if any changed file lies outside the allowed paths, then runs the configured checks in order and stops at the first failure, " +
		"returning its output so you can fix the problem and call it again. On the base branch it creates a new task branch; on a task branch it adds to that branch. " +
		"After a successful submit the workspace stays on the task branch, so a follow-up change to the same task is submitted the same way. " +
		"Before starting an unrelated task, call this tool with new_task=true while the workspace has no uncommitted changes, then make the new changes. " +
		"Relay the returned receipt to the user in full, without restating the check results in your own words."
}

func (t *Tool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":       map[string]any{"type": "string", "description": "One sentence describing the change."},
			"new_task":      map[string]any{"type": "boolean", "description": "Switch a clean workspace back to the base branch to start a new, unrelated task."},
			"preview_paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional site paths to show under the preview URL, such as /news/<slug>/."},
		},
		"required": []string{"summary"},
	}
}

func (t *Tool) Validate(raw json.RawMessage) (json.RawMessage, error) {
	var p Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	p.Summary = strings.TrimSpace(p.Summary)
	if p.Summary == "" && !p.NewTask {
		return nil, errors.New("summary 不能为空：用一句话说明这次改动")
	}
	if len([]rune(p.Summary)) > maxSummary {
		return nil, fmt.Errorf("summary 过长（上限 %d 字）", maxSummary)
	}
	if len(p.PreviewPaths) > 10 {
		return nil, errors.New("preview_paths 最多 10 项")
	}
	for _, path := range p.PreviewPaths {
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \t\r\n") {
			return nil, fmt.Errorf("preview_paths 必须是以 / 开头的站内路径：%q", path)
		}
	}
	return json.Marshal(p)
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage, _ func(agent.PartialResult)) (agent.ToolResult, error) {
	var p Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	// Submits to one workspace are sequential: two tasks editing the same
	// checkout must not stage each other's files.
	lock := workspaceLock(t.workspace)
	lock.Lock()
	defer lock.Unlock()

	var (
		r   *receipt
		err error
	)
	if p.NewTask {
		r, err = t.startTask(ctx)
	} else {
		r, err = t.submit(ctx, p)
	}
	if err != nil {
		message := err.Error()
		return agent.ToolResult{IsError: true, Content: message, UserFacing: "❌ " + firstLine(message)}, err
	}
	text := r.markdown()
	return agent.ToolResult{
		Content:    text,
		UserFacing: text,
		Details:    map[string]any{"kind": DetailsKind, "receipt": r},
	}, nil
}

// submit validates, gates, commits and pushes the working tree's changes.
func (t *Tool) submit(ctx context.Context, p Params) (*receipt, error) {
	r := &receipt{Summary: p.Summary}
	changes, err := t.status(ctx)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		// Nothing new to commit. On a task branch that can mean an earlier
		// commit never reached the remote, so push again instead of refusing.
		if r, err = t.pushPending(ctx); r != nil || err != nil {
			return r, err
		}
		return nil, errors.New("没有可提交的改动：先完成修改，再调用 submit_task")
	}
	if err = t.checkAllowed(changes); err != nil {
		return nil, err
	}
	if r.Gate, err = t.runGate(ctx); err != nil {
		return nil, err
	}
	// The gate can add, remove or rewrite files: judge and stage what exists now.
	if changes, err = t.status(ctx); err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, errors.New("质检之后没有可提交的改动")
	}
	if err = t.checkAllowed(changes); err != nil {
		return nil, err
	}
	if r.Branch, err = t.currentBranch(ctx); err != nil {
		return nil, err
	}
	if !t.isTaskBranch(r.Branch) {
		r.Warnings = append(r.Warnings, t.staleBaseWarnings(ctx)...)
		created := t.branchName(ctx, p.Summary)
		if _, err = t.git(ctx, gitTimeout, "switch", "-c", created); err != nil {
			return nil, fmt.Errorf("创建任务分支失败: %w", err)
		}
		r.Branch = created
	}
	if _, err = t.git(ctx, gitTimeout, append([]string{"add", "-A", "--"}, changedPaths(changes)...)...); err != nil {
		return nil, fmt.Errorf("暂存改动失败: %w", err)
	}
	if _, err = t.git(ctx, gitTimeout, "commit", "-m", p.Summary, "-m", "Submitted by EasyAgent submit_task."); err != nil {
		return nil, fmt.Errorf("提交失败: %w", err)
	}
	if r.Commit, err = t.git(ctx, gitTimeout, "rev-parse", "--short", "HEAD"); err != nil {
		return nil, err
	}
	if r.Files, err = t.lastCommitFiles(ctx); err != nil {
		return nil, err
	}
	if !t.cfg.PushEnabled() {
		r.Preview, r.NextSteps = t.previewLinks(p.PreviewPaths), t.nextSteps()
		return r, nil
	}
	if err = t.push(ctx, r.Branch); err != nil {
		return nil, fmt.Errorf("改动已提交到本地分支 %s（%s），但没有推送: %w。排除原因后再次调用 submit_task 会重试推送", r.Branch, r.Commit, err)
	}
	r.Pushed = true
	if mr := t.cfg.MR; mr != nil {
		if r.MRURL, err = t.mergeRequest(ctx, *mr, r.Branch, p.Summary); err != nil {
			r.Warnings = append(r.Warnings, "合并请求创建失败："+err.Error())
		}
	}
	r.Preview, r.NextSteps = t.previewLinks(p.PreviewPaths), t.nextSteps()
	return r, nil
}

// startTask returns a clean workspace to an up-to-date base branch.
func (t *Tool) startTask(ctx context.Context) (*receipt, error) {
	changes, err := t.status(ctx)
	if err != nil {
		return nil, err
	}
	if len(changes) > 0 {
		return nil, fmt.Errorf("开始新任务前工作区必须没有未提交的改动（当前有 %d 处）。这些改动若属于当前任务，请先不带 new_task 调用 submit_task 提交；否则先撤销它们", len(changes))
	}
	base, remote := t.baseBranch(), t.remote()
	r := &receipt{Ready: true, Branch: base, NextSteps: "现在可以开始新任务：完成修改后调用 submit_task 提交。"}
	// Fetch first: git switch then creates a missing local base from the remote branch.
	if _, err = t.git(ctx, pushTimeout, "fetch", remote, base); err != nil {
		r.Warnings = append(r.Warnings, "拉取远端 "+remote+"/"+base+" 失败，新任务将基于本地 "+base)
	}
	current, err := t.currentBranch(ctx)
	if err != nil {
		return nil, err
	}
	if current != base {
		if _, err = t.git(ctx, gitTimeout, "switch", base); err != nil {
			return nil, fmt.Errorf("切回主分支 %s 失败: %w", base, err)
		}
	}
	if len(r.Warnings) == 0 {
		if _, err = t.git(ctx, gitTimeout, "merge", "--ff-only", remote+"/"+base); err != nil {
			r.Warnings = append(r.Warnings, "本地 "+base+" 未能快进到远端最新，新任务将基于旧的 "+base)
		}
	}
	return r, nil
}

// pushPending pushes a task branch whose commits the remote still lacks. It
// returns nil, nil when nothing is pending.
func (t *Tool) pushPending(ctx context.Context) (*receipt, error) {
	if !t.cfg.PushEnabled() {
		return nil, nil
	}
	branch, err := t.currentBranch(ctx)
	if err != nil || !t.isTaskBranch(branch) {
		return nil, nil
	}
	// Without an upstream the branch has never been pushed. With one, the
	// remote branch is the reference; if it is gone, the work already landed.
	ahead := t.baseBranch() + ".." + branch
	if upstream, err := t.git(ctx, gitTimeout, "rev-parse", "--abbrev-ref", branch+"@{upstream}"); err == nil {
		if _, err = t.git(ctx, pushTimeout, "fetch", t.remote(), branch); err != nil {
			return nil, nil
		}
		ahead = strings.TrimSpace(upstream) + ".." + branch
	}
	count, err := t.git(ctx, gitTimeout, "rev-list", "--count", ahead)
	if err != nil || strings.TrimSpace(count) == "0" {
		return nil, nil
	}
	if err = t.push(ctx, branch); err != nil {
		return nil, fmt.Errorf("分支 %s 上有尚未推送的提交: %w", branch, err)
	}
	r := &receipt{Summary: "推送之前已提交的改动", Branch: branch, Pushed: true, Gate: []gateStep{}, NextSteps: t.nextSteps()}
	if r.Commit, err = t.git(ctx, gitTimeout, "rev-parse", "--short", "HEAD"); err != nil {
		return nil, err
	}
	if files, err := t.git(ctx, gitTimeout, "diff", "--name-status", ahead, "HEAD"); err == nil {
		r.Files = parseNameStatus(files)
	}
	return r, nil
}

func (t *Tool) remoteBranch(branch string) string { return t.remote() + "/" + branch }

func (t *Tool) remote() string {
	if t.cfg.Remote != "" {
		return t.cfg.Remote
	}
	return defaultRemote
}

func (t *Tool) baseBranch() string {
	if t.cfg.BaseBranch != "" {
		return t.cfg.BaseBranch
	}
	return defaultBase
}

func (t *Tool) nextSteps() string {
	if t.cfg.NextSteps != "" {
		return t.cfg.NextSteps
	}
	return defaultNextStep
}

func (t *Tool) isTaskBranch(branch string) bool {
	for _, prefix := range t.cfg.TaskBranchPrefixes() {
		if len(branch) > len(prefix) && strings.HasPrefix(branch, prefix) {
			return true
		}
	}
	return false
}

// staleBaseWarnings tells when a new task branch starts from a base that is
// behind the remote; the merge then needs attention.
func (t *Tool) staleBaseWarnings(ctx context.Context) []string {
	base := t.baseBranch()
	if _, err := t.git(ctx, pushTimeout, "fetch", t.remote(), base); err != nil {
		return []string{"拉取远端 " + t.remoteBranch(base) + " 失败，任务分支基于本地 " + base}
	}
	behind, err := t.git(ctx, gitTimeout, "rev-list", "--count", "HEAD.."+t.remoteBranch(base))
	if err != nil || strings.TrimSpace(behind) == "0" {
		return nil
	}
	return []string{"本地 " + base + " 落后远端 " + behind + " 个提交，任务分支基于旧的 " + base + "，合并时可能需要处理冲突"}
}

// branchName derives a task branch from the summary and makes it unique.
func (t *Tool) branchName(ctx context.Context, summary string) string {
	prefix := t.cfg.TaskBranchPrefixes()[0]
	base := prefix + slug(summary) + "-" + t.now().Format("20060102-150405")
	for candidate, i := base, 2; ; candidate, i = fmt.Sprintf("%s-%d", base, i), i+1 {
		if _, err := t.git(ctx, gitTimeout, "show-ref", "--verify", "--quiet", "refs/heads/"+candidate); err != nil {
			return candidate
		}
	}
}

func (t *Tool) currentBranch(ctx context.Context) (string, error) {
	branch, err := t.git(ctx, gitTimeout, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("读取当前分支失败，工作区不是 Git 仓库？: %w", err)
	}
	if branch == "HEAD" {
		return "", errors.New("工作区处于游离 HEAD 状态，无法提交；请先切回一个分支")
	}
	return branch, nil
}

// push publishes a task branch. It never forces, so a branch rewritten on the
// remote is rejected here and left for a person to sort out.
func (t *Tool) push(ctx context.Context, branch string) error {
	_, err := t.git(ctx, pushTimeout, "push", "-u", t.remote(), branch)
	if err != nil {
		return fmt.Errorf("git push %s %s: %w", t.remote(), branch, err)
	}
	return nil
}

// change is one entry of git status.
type change struct {
	status string
	path   string
}

func (t *Tool) status(ctx context.Context) ([]change, error) {
	// -z keeps unusual paths intact and lists every untracked file on its own.
	out, err := t.git(ctx, gitTimeout, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("读取工作区状态失败: %w", err)
	}
	return parseStatus(out), nil
}

// parseStatus reads the NUL-separated records of `git status --porcelain -z`.
// A rename or copy is followed by the path it came from.
func parseStatus(out string) []change {
	var changes []change
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue
		}
		code, path := entry[:2], entry[3:]
		changes = append(changes, change{status: strings.TrimSpace(code), path: path})
		if strings.ContainsAny(code, "RC") && i+1 < len(fields) {
			changes = append(changes, change{status: "from", path: fields[i+1]})
			i++
		}
	}
	return changes
}

// checkAllowed refuses the submit when a change lies outside the included paths.
func (t *Tool) checkAllowed(changes []change) error {
	if len(t.allow.Allow) == 0 {
		return nil
	}
	var outside []string
	for _, c := range changes {
		allowed := false
		for _, pattern := range t.allow.Allow {
			if allowed = tools.MatchPathGlob(pattern, filepath.ToSlash(c.path)); allowed {
				break
			}
		}
		if !allowed {
			outside = append(outside, c.path)
		}
	}
	if len(outside) == 0 {
		return nil
	}
	return fmt.Errorf("有 %d 处改动不在允许提交的路径内（%s），已拦截，没有创建分支也没有推送。请先把这些改动恢复原状，再重新调用 submit_task：%s",
		len(outside), strings.Join(t.allow.Allow, ", "), listPaths(outside))
}

func changedPaths(changes []change) []string {
	seen := make(map[string]bool, len(changes))
	paths := make([]string, 0, len(changes))
	for _, c := range changes {
		if !seen[c.path] {
			seen[c.path] = true
			paths = append(paths, c.path)
		}
	}
	return paths
}

// runGate runs the configured checks in order and stops at the first failure.
func (t *Tool) runGate(ctx context.Context) ([]gateStep, error) {
	steps := make([]gateStep, 0, len(t.cfg.Gate))
	for _, command := range t.cfg.Gate {
		started := time.Now()
		out, err := t.run(ctx, t.cfg.GateTimeout(), command)
		step := gateStep{Command: command, OK: err == nil, DurationMS: time.Since(started).Milliseconds()}
		steps = append(steps, step)
		if err != nil {
			step.Output = tail(out)
			if step.Output == "" {
				step.Output = err.Error()
			}
			return steps, fmt.Errorf("质检未通过，已拦截提交（没有创建分支，也没有推送）。失败的步骤：%s\n%s\n请修复后重新调用 submit_task", command, step.Output)
		}
	}
	return steps, nil
}

func (t *Tool) lastCommitFiles(ctx context.Context) ([]fileChange, error) {
	out, err := t.git(ctx, gitTimeout, "diff-tree", "--no-commit-id", "--name-status", "-r", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("读取提交内容失败: %w", err)
	}
	return parseNameStatus(out), nil
}

// parseNameStatus reads `--name-status` output; a rename lists both paths.
func parseNameStatus(out string) []fileChange {
	var files []fileChange
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		status := fields[0][:1]
		if status == "R" || status == "C" {
			status = "M"
		}
		files = append(files, fileChange{Status: status, Path: fields[len(fields)-1]})
	}
	return files
}

func (t *Tool) previewLinks(paths []string) []string {
	if t.cfg.PreviewURL == "" {
		return nil
	}
	links := make([]string, 0, len(paths))
	base := strings.TrimRight(t.cfg.PreviewURL, "/")
	for _, path := range paths {
		links = append(links, base+path)
	}
	return links
}

// run executes one command in the workspace. Git must never wait for a
// credential prompt, because there is nobody to answer it.
func (t *Tool) run(ctx context.Context, timeout time.Duration, command string) (string, error) {
	res, err := t.ops.Run(ctx, operations.RunRequest{Command: command, WorkDir: t.workspace, Timeout: timeout, Env: []string{"GIT_TERMINAL_PROMPT=0"}})
	out := strings.TrimRight(string(res.Output), "\n")
	if err != nil {
		return out, &commandError{command: command, output: out, cause: err}
	}
	if res.ExitCode != 0 {
		return out, &commandError{command: command, output: out}
	}
	return out, nil
}

// git runs git with its arguments quoted as separate words.
func (t *Tool) git(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return t.run(ctx, timeout, "git "+strings.Join(quoted, " "))
}

type commandError struct {
	command string
	output  string
	cause   error
}

func (e *commandError) Error() string {
	if e.cause != nil {
		if e.output != "" {
			return e.command + " 没有完成（" + e.cause.Error() + "）：" + tail(e.output)
		}
		return e.command + " 没有完成（" + e.cause.Error() + "）"
	}
	if e.output == "" {
		return e.command + " 失败"
	}
	return e.command + " 失败：" + tail(e.output)
}

func (e *commandError) Unwrap() error { return e.cause }

var workspaceLocks = struct {
	sync.Mutex
	locks map[string]*sync.Mutex
}{locks: map[string]*sync.Mutex{}}

// workspaceLock serialises submits that share a checkout.
func workspaceLock(workspace string) *sync.Mutex {
	workspaceLocks.Lock()
	defer workspaceLocks.Unlock()
	if workspaceLocks.locks[workspace] == nil {
		workspaceLocks.locks[workspace] = &sync.Mutex{}
	}
	return workspaceLocks.locks[workspace]
}

var slugPart = regexp.MustCompile(`[^a-z0-9]+`)

// slug reduces a summary to the ASCII letters and digits of a branch name.
func slug(summary string) string {
	s := strings.Trim(slugPart.ReplaceAllString(strings.ToLower(summary), "-"), "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	if s == "" {
		return "task"
	}
	return s
}

// mergeRequest creates a merge request for the task branch, or finds the
// open one when the branch already has it.
func (t *Tool) mergeRequest(ctx context.Context, cfg config.MergeRequestConfig, branch, summary string) (string, error) {
	token := os.Getenv(cfg.TokenEnv)
	if token == "" {
		return "", fmt.Errorf("环境变量 %s 未设置", cfg.TokenEnv)
	}
	var created struct {
		WebURL string `json:"web_url"`
	}
	payload := map[string]any{
		"source_branch": branch, "target_branch": t.baseBranch(), "title": cfg.TitlePrefix + summary,
		"description": summary + "\n\nSubmitted by EasyAgent submit_task.", "remove_source_branch": true,
	}
	status, err := t.gitlab(ctx, cfg, token, http.MethodPost, "/merge_requests", payload, &created)
	if err == nil && created.WebURL != "" {
		return created.WebURL, nil
	}
	if err != nil && status != http.StatusConflict {
		return "", err
	}
	return t.existingMergeRequest(ctx, cfg, token, branch)
}

func (t *Tool) existingMergeRequest(ctx context.Context, cfg config.MergeRequestConfig, token, branch string) (string, error) {
	var opened []struct {
		WebURL string `json:"web_url"`
	}
	query := "/merge_requests?source_branch=" + url.QueryEscape(branch) + "&state=opened"
	if _, err := t.gitlab(ctx, cfg, token, http.MethodGet, query, nil, &opened); err != nil {
		return "", err
	}
	if len(opened) == 0 || opened[0].WebURL == "" {
		return "", errors.New("GitLab 没有返回合并请求地址")
	}
	return opened[0].WebURL, nil
}

func (t *Tool) gitlab(ctx context.Context, cfg config.MergeRequestConfig, token, method, query string, payload any, out any) (int, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(data)
	}
	endpoint := strings.TrimRight(cfg.APIURL, "/") + "/projects/" + url.PathEscape(cfg.Project) + query
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req) // #nosec G107 -- the API root and project come from the operator's configuration.
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("GitLab 返回 %d: %s", resp.StatusCode, tail(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("GitLab 响应无法解析: %w", err)
		}
	}
	return resp.StatusCode, nil
}
