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
// be committed; it must be explicit and nonempty. ops runs git and the gate.
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
	validated, err := t.Validate(raw)
	if err != nil {
		return agent.ToolResult{IsError: true, Content: err.Error()}, err
	}
	if err = t.validateConfig(); err != nil {
		return agent.ToolResult{IsError: true, Content: err.Error()}, err
	}
	var p Params
	if err = json.Unmarshal(validated, &p); err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	// Submits to one workspace are sequential: two tasks editing the same
	// checkout must not stage each other's files.
	lock := workspaceLock(t.workspace)
	lock.Lock()
	defer lock.Unlock()

	var r *receipt
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

func (t *Tool) validateConfig() error {
	if len(t.cfg.Gate) == 0 {
		return errors.New("submit_task requires a nonempty gate")
	}
	for _, command := range t.cfg.Gate {
		if strings.TrimSpace(command) == "" {
			return errors.New("submit_task gate commands must not be empty")
		}
	}
	if len(t.allow.Allow) == 0 {
		return errors.New("submit_task requires an explicit nonempty include allowlist")
	}
	for _, pattern := range t.allow.Allow {
		if strings.TrimSpace(pattern) == "" {
			return errors.New("submit_task include patterns must not be empty")
		}
	}
	for _, prefix := range t.cfg.TaskBranchPrefixes() {
		if strings.TrimSpace(prefix) == "" {
			return errors.New("submit_task branch prefixes must not be empty")
		}
	}
	return nil
}

// submit uses the same gate and complete history audit for new commits and retries.
func (t *Tool) submit(ctx context.Context, p Params) (*receipt, error) {
	r := &receipt{Summary: p.Summary}
	branch, err := t.currentBranch(ctx)
	if err != nil {
		return nil, err
	}
	if branch != t.baseBranch() && !t.isTaskBranch(branch) {
		return nil, fmt.Errorf("拒绝从任意分支 %s 提交；请使用配置的 base 或任务分支", branch)
	}
	base, err := t.submissionBase(ctx, branch)
	if err != nil {
		return nil, err
	}
	changes, err := t.status(ctx)
	if err != nil {
		return nil, err
	}
	if err = t.checkAllowed(changes); err != nil {
		return nil, err
	}
	if _, err = t.auditHistory(ctx, base); err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		pending, err := t.hasPending(ctx, branch)
		if err != nil {
			return nil, err
		}
		if !pending {
			return nil, errors.New("没有可提交的改动：先完成修改，再调用 submit_task")
		}
	}
	// Stage literal filenames before the gate. The index tree is the exact
	// candidate commit, and both it and the worktree must remain unchanged.
	if len(changes) > 0 {
		// Every status entry was audited, including already-staged deletions
		// and rename sources that no longer exist in the index.
		if _, err = t.git(ctx, gitTimeout, "--literal-pathspecs", "add", "-A", "--", "."); err != nil {
			return nil, fmt.Errorf("暂存改动失败: %w", err)
		}
	}
	staged, err := t.git(ctx, gitTimeout, "diff", "--cached", "--no-renames", "--name-status", "-z", base, "--")
	if err != nil {
		return nil, err
	}
	if err = t.checkAllowed(fileChanges(parseNameStatus(staged))); err != nil {
		return nil, err
	}
	snapshot, err := t.sourceState(ctx)
	if err != nil {
		return nil, err
	}
	if r.Gate, err = t.runGate(ctx, snapshot); err != nil {
		return nil, err
	}
	if err = t.checkSourceState(ctx, snapshot); err != nil {
		return nil, err
	}
	r.Branch = branch
	if len(changes) > 0 {
		if branch == t.baseBranch() {
			created := t.branchName(ctx, p.Summary)
			if !t.isTaskBranch(created) {
				return nil, errors.New("生成的任务分支不安全")
			}
			if _, err = t.git(ctx, gitTimeout, "switch", "-c", created); err != nil {
				return nil, fmt.Errorf("创建任务分支失败: %w", err)
			}
			r.Branch = created
		}
		if _, err = t.git(ctx, gitTimeout, "-c", "core.hooksPath=/dev/null", "commit", "-m", p.Summary, "-m", "Submitted by EasyAgent submit_task."); err != nil {
			return nil, fmt.Errorf("提交失败: %w", err)
		}
		// Do not publish if anything other than the tested tree was committed.
		tree, err := t.git(ctx, gitTimeout, "rev-parse", "HEAD^{tree}")
		if err != nil || tree != snapshot.tree {
			return nil, errors.New("提交的源码树与质检时不同，已拦截推送")
		}
	}
	if r.Files, err = t.auditHistory(ctx, base); err != nil {
		return nil, err
	}
	if r.Commit, err = t.git(ctx, gitTimeout, "rev-parse", "--short", "HEAD"); err != nil {
		return nil, err
	}
	if len(changes) > 0 {
		if r.Files, err = t.lastCommitFiles(ctx); err != nil {
			return nil, err
		}
	}
	if !t.cfg.PushEnabled() {
		r.Preview, r.NextSteps = t.previewLinks(p.PreviewPaths), t.nextSteps()
		return r, nil
	}
	verified := snapshot
	verified.branch = r.Branch
	if verified.head, err = t.git(ctx, gitTimeout, "rev-parse", "HEAD"); err != nil {
		return nil, err
	}
	if err = t.checkSourceState(ctx, verified); err != nil {
		return nil, err
	}
	if err = t.push(ctx, r.Branch, verified.head); err != nil {
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
	base := t.baseBranch()
	current, err := t.currentBranch(ctx)
	if err != nil {
		return nil, err
	}
	if current != base && !t.isTaskBranch(current) {
		return nil, errors.New("只能从 base 或任务分支开始新任务")
	}
	localBase, err := t.git(ctx, gitTimeout, "rev-parse", "refs/heads/"+base)
	if err != nil {
		return nil, err
	}
	latest := localBase
	if t.cfg.PushEnabled() {
		if latest, err = t.fetchBase(ctx); err != nil {
			return nil, err
		}
		if _, err = t.git(ctx, gitTimeout, "merge-base", "--is-ancestor", localBase, latest); err != nil {
			return nil, errors.New("本地 base 不能快进到远端最新；保留当前分支")
		}
	}
	if current != base {
		// A new task must not hide local-only commits, including offline demos.
		if _, merged := t.git(ctx, gitTimeout, "merge-base", "--is-ancestor", "HEAD", latest); merged != nil {
			if !t.cfg.PushEnabled() {
				return nil, errors.New("当前任务还有未推送的提交；保留当前分支")
			}
			pending, err := t.hasPending(ctx, current)
			if err != nil {
				return nil, err
			}
			if pending {
				return nil, errors.New("当前任务还有未推送的提交；请先重试提交，保留当前分支")
			}
		}
	}
	if _, err = t.git(ctx, gitTimeout, "switch", base); err != nil {
		return nil, fmt.Errorf("切回主分支 %s 失败: %w", base, err)
	}
	if latest != localBase {
		if _, err = t.git(ctx, gitTimeout, "merge", "--ff-only", latest); err != nil {
			return nil, fmt.Errorf("更新 base 失败: %w", err)
		}
	}
	return &receipt{Ready: true, Branch: base, NextSteps: "现在可以开始新任务：完成修改后调用 submit_task 提交。"}, nil
}

// fetchBase uses an explicit heads ref, never a possibly ambiguous tag or FETCH_HEAD.
func (t *Tool) fetchBase(ctx context.Context) (string, error) {
	ref := "refs/remotes/" + t.remote() + "/" + t.baseBranch()
	if _, err := t.git(ctx, pushTimeout, "fetch", "--no-tags", "--", t.remote(), "+refs/heads/"+t.baseBranch()+":"+ref); err != nil {
		return "", fmt.Errorf("无法确认远端 base 最新状态，已拦截: %w", err)
	}
	return t.git(ctx, gitTimeout, "rev-parse", ref+"^{commit}")
}

func (t *Tool) submissionBase(ctx context.Context, branch string) (string, error) {
	base, err := t.git(ctx, gitTimeout, "rev-parse", "refs/heads/"+t.baseBranch()+"^{commit}")
	if err != nil {
		return "", err
	}
	if t.cfg.PushEnabled() {
		if base, err = t.fetchBase(ctx); err != nil {
			return "", err
		}
	}
	head, err := t.git(ctx, gitTimeout, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if branch == t.baseBranch() {
		if head != base {
			return "", errors.New("本地 base 与远端最新不同，已拦截；请先更新 base，不得携带本地额外提交")
		}
	} else if _, err = t.git(ctx, gitTimeout, "merge-base", "--is-ancestor", base, head); err != nil {
		return "", errors.New("任务分支未包含最新 base，已拦截；请先更新任务分支")
	}
	return base, nil
}

func (t *Tool) hasPending(ctx context.Context, branch string) (bool, error) {
	if !t.cfg.PushEnabled() || !t.isTaskBranch(branch) {
		return false, nil
	}
	ref := "refs/heads/" + branch
	out, err := t.git(ctx, pushTimeout, "ls-remote", "--heads", "--", t.remote(), ref)
	if err != nil {
		return false, err
	}
	if out == "" {
		return true, nil
	}
	tracking := "refs/remotes/" + t.remote() + "/" + branch
	if _, err = t.git(ctx, pushTimeout, "fetch", "--no-tags", "--", t.remote(), "+"+ref+":"+tracking); err != nil {
		return false, err
	}
	_, err = t.git(ctx, gitTimeout, "merge-base", "--is-ancestor", "HEAD", tracking)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, nil
	}
	return false, nil
}

// Audit every commit, not just the final diff: a protected edit followed by a
// revert is still protected history that would be published. Disabling rename
// detection reports both the removed source and added destination, NUL-delimited.
func (t *Tool) auditHistory(ctx context.Context, base string) ([]fileChange, error) {
	commits, err := t.git(ctx, gitTimeout, "rev-list", base+"..HEAD", "--")
	if err != nil {
		return nil, err
	}
	var files []fileChange
	for _, commit := range strings.Fields(commits) {
		out, err := t.git(ctx, gitTimeout, "diff-tree", "--root", "-m", "--no-commit-id", "--no-renames", "--name-status", "-z", "-r", commit, "--")
		if err != nil {
			return nil, err
		}
		files = append(files, parseNameStatus(out)...)
	}
	return files, t.checkAllowed(fileChanges(files))
}

func fileChanges(files []fileChange) []change {
	changes := make([]change, 0, len(files))
	for _, file := range files {
		changes = append(changes, change{status: file.Status, path: file.Path})
	}
	return changes
}

type sourceState struct {
	head, tree, branch string
}

func (t *Tool) sourceState(ctx context.Context) (sourceState, error) {
	var s sourceState
	var err error
	if s.head, err = t.git(ctx, gitTimeout, "rev-parse", "HEAD"); err != nil {
		return s, err
	}
	if s.branch, err = t.currentBranch(ctx); err != nil {
		return s, err
	}
	if s.tree, err = t.git(ctx, gitTimeout, "write-tree"); err != nil {
		return s, err
	}
	if _, err = t.git(ctx, gitTimeout, "diff", "--no-ext-diff", "--quiet", "--"); err != nil {
		return s, errors.New("源码与暂存树不同，已拦截；请重新质检")
	}
	out, err := t.git(ctx, gitTimeout, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return s, err
	}
	if out != "" {
		return s, errors.New("源码出现未质检的新文件，已拦截")
	}
	return s, nil
}

func (t *Tool) checkSourceState(ctx context.Context, expected sourceState) error {
	actual, err := t.sourceState(ctx)
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("质检期间源码、提交或分支发生变化，已拦截；请重新质检")
	}
	return nil
}

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
	if branch == "" || branch == "HEAD" || branch == t.baseBranch() || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "refs/") {
		return false
	}
	for _, prefix := range t.cfg.TaskBranchPrefixes() {
		if prefix != "" && len(branch) > len(prefix) && strings.HasPrefix(branch, prefix) {
			return true
		}
	}
	return false
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
	ref, err := t.git(ctx, gitTimeout, "symbolic-ref", "--quiet", "HEAD")
	branch := strings.TrimPrefix(ref, "refs/heads/")
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
func (t *Tool) push(ctx context.Context, branch, commit string) error {
	if !t.isTaskBranch(branch) {
		return errors.New("拒绝推送非任务分支")
	}
	if _, err := t.git(ctx, gitTimeout, "check-ref-format", "refs/heads/"+branch); err != nil {
		return err
	}
	actual, err := t.git(ctx, gitTimeout, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil || actual != commit {
		return errors.New("任务分支与已质检提交不同，已拦截推送")
	}
	// Explicit heads destination and pinned commit prevent tags, HEAD, push
	// defaults and followTags from expanding what gets published.
	_, err = t.git(ctx, pushTimeout, "-c", "core.hooksPath=/dev/null", "push", "--no-follow-tags", "--", t.remote(), commit+":refs/heads/"+branch)
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
		return errors.New("submit_task requires an explicit nonempty include allowlist")
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

// runGate runs the configured checks in order and stops at the first failure.
func (t *Tool) runGate(ctx context.Context, source sourceState) ([]gateStep, error) {
	if err := t.validateConfig(); err != nil {
		return nil, err
	}
	steps := make([]gateStep, 0, len(t.cfg.Gate))
	for _, command := range t.cfg.Gate {
		started := time.Now()
		out, err := t.run(ctx, t.cfg.GateTimeout(), command)
		step := gateStep{Command: command, OK: err == nil, DurationMS: time.Since(started).Milliseconds()}
		steps = append(steps, step)
		if err != nil {
			if ctx.Err() != nil {
				return steps, ctx.Err()
			}
			step.Output = tail(out)
			if step.Output == "" {
				step.Output = err.Error()
			}
			return steps, fmt.Errorf("质检未通过，已拦截提交（没有创建分支，也没有推送）。失败的步骤：%s\n%s\n请修复后重新调用 submit_task", command, step.Output)
		}
		if err = t.checkSourceState(ctx, source); err != nil {
			return steps, err
		}
	}
	return steps, nil
}

func (t *Tool) lastCommitFiles(ctx context.Context) ([]fileChange, error) {
	out, err := t.git(ctx, gitTimeout, "diff-tree", "--no-commit-id", "--no-renames", "--name-status", "-z", "-r", "HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("读取提交内容失败: %w", err)
	}
	return parseNameStatus(out), nil
}

// parseNameStatus reads --no-renames --name-status -z without decoding or
// splitting filenames on whitespace (including non-ASCII names and newlines).
func parseNameStatus(out string) []fileChange {
	var files []fileChange
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] != "" {
			files = append(files, fileChange{Status: fields[i][:1], Path: fields[i+1]})
		}
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	res, err := t.ops.Run(ctx, operations.RunRequest{Command: command, WorkDir: t.workspace, Timeout: timeout, Env: []string{"GIT_TERMINAL_PROMPT=0"}})
	out := strings.TrimRight(string(res.Output), "\n")
	if err == nil {
		err = ctx.Err()
	}
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
