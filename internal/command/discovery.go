package command

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GitRunner runs a git command in a repository and returns trimmed stdout.
// It is an interface point for tests; production uses ExecGit.
type GitRunner func(ctx context.Context, repoPath string, args ...string) (string, error)

// ExecGit runs git in repoPath via os/exec.
func ExecGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	full := append([]string{"-C", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// goalBranchRe matches the goal/<N>[-rest] branch convention.
var goalBranchRe = regexp.MustCompile(`^goal/(\d+)(?:[-_](.*))?$`)

// closesRe matches "Closes #12" (also Fixes/Closes/Resolves, case-insensitive)
// anywhere in a commit subject.
var closesRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+#(\d+)\b`)

// ScanReport summarizes one discovery pass.
type ScanReport struct {
	NewResults int      `json:"new_results"`
	NewInbox   int      `json:"new_inbox"`
	SpaceErrs  []string `json:"space_errs,omitempty"`
}

// ScanAll runs discovery over every space that has a repository.
// For each goal/N-* branch not yet known:
//   - N matches a goal → result attached to that goal; open goals also get an
//     inbox item (「新结果」)
//   - N unknown → unassigned result + inbox item (「未归属的结果」)
//
// Commit subjects containing Closes/Fixes/Resolves #N add commit results for
// known goals. References to unknown goals are ignored: branches are the
// primary convention, a stray "Closes #N" is weak evidence.
func (s *Store) ScanAll(ctx context.Context, run GitRunner) ScanReport {
	var report ScanReport
	for _, sp := range s.Spaces() {
		if sp.RepoPath == "" {
			continue
		}
		if err := s.scanSpace(ctx, sp, run, &report); err != nil {
			report.SpaceErrs = append(report.SpaceErrs,
				fmt.Sprintf("%s: %v", sp.Name, err))
		}
	}
	return report
}

func (s *Store) scanSpace(ctx context.Context, sp Space, run GitRunner, report *ScanReport) error {
	out, err := run(ctx, sp.RepoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return err
	}
	now := time.Now()
	knownGoal := func(n int) (Goal, bool) {
		g, err := s.Goal(n)
		return g, err == nil
	}
	for _, branch := range strings.Split(out, "\n") {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			continue
		}
		m := goalBranchRe.FindStringSubmatch(branch)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		res := Result{
			ID:           "branch:" + sp.ID + ":" + branch,
			SpaceID:      sp.ID,
			Kind:         ResultBranch,
			Ref:          branch,
			DiscoveredAt: now,
		}
		if goal, ok := knownGoal(n); ok {
			res.GoalNumber = goal.Number
			res.Title = strings.TrimSpace(m[2])
			added, err := s.AddResult(res)
			if err != nil {
				return err
			}
			if added {
				report.NewResults++
				if goal.Status == GoalOpen {
					if _, err := s.AddInboxItem(InboxItem{
						Kind:       InboxResult,
						Title:      fmt.Sprintf("目标 #%d 有新结果", goal.Number),
						Body:       branch,
						SpaceID:    sp.ID,
						GoalNumber: goal.Number,
						ResultID:   res.ID,
					}); err == nil {
						report.NewInbox++
					}
				}
			}
			continue
		}
		added, err := s.AddResult(res)
		if err != nil {
			return err
		}
		if added {
			report.NewResults++
			if _, err := s.AddInboxItem(InboxItem{
				Kind:     InboxUnassigned,
				Title:    fmt.Sprintf("未归属的结果：%s", branch),
				Body:     fmt.Sprintf("git 发现分支 goal/%s，没有对应目标", m[1]),
				SpaceID:  sp.ID,
				ResultID: res.ID,
			}); err == nil {
				report.NewInbox++
			}
		}
	}

	// Commits referencing goals (Closes/Fixes/Resolves #N).
	// Non-fatal: branches are the primary convention.
	out, err = run(ctx, sp.RepoPath, "log", "--all", "--format=%h %s", "--grep=(?i)(close[sd]?|fix(e[sd])?|resolve[sd]?) #[0-9]+", "-E")
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cm := closesRe.FindStringSubmatch(line)
		if cm == nil {
			continue
		}
		sha := strings.Fields(line)[0]
		n, _ := strconv.Atoi(cm[1])
		if _, ok := knownGoal(n); !ok {
			continue
		}
		res := Result{
			ID:           "commit:" + sp.ID + ":" + sha,
			SpaceID:      sp.ID,
			GoalNumber:   n,
			Kind:         ResultCommit,
			Ref:          sha,
			Title:        strings.TrimSpace(strings.TrimPrefix(line, sha+" ")),
			DiscoveredAt: now,
		}
		added, err := s.AddResult(res)
		if err != nil {
			return err
		}
		if added {
			report.NewResults++
		}
	}
	return nil
}
