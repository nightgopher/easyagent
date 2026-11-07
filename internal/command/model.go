// Package command implements the personal command center: spaces, goals and
// results with git-based result discovery and an inbox.
//
// Design: docs/decisions/personal-command-center.md.
// 空间 → 目标（≈ issue）→ 结果（≈ PR / 交付物）；会话是过程，不在此管理。
package command

import "time"

// Space is a long-running context for one area of work (a project, a job
// search, learning a language). It maps to at most one local git repository
// whose branches feed result discovery.
type Space struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	RepoPath  string    `json:"repo_path,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Goal statuses.
const (
	GoalOpen   = "open"
	GoalClosed = "closed"
)

// DoneItem is one line of a goal's definition of done.
type DoneItem struct {
	Text  string `json:"text"`
	Check bool   `json:"check"`
}

// Goal is what the user wants achieved, roughly an issue.
type Goal struct {
	Number      int        `json:"number"` // 目标编号，#N
	SpaceID     string     `json:"space_id"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Done        []DoneItem `json:"done"` // 完成标准
	Status      string     `json:"status"`
	Deadline    *time.Time `json:"deadline,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Result kinds.
const (
	ResultBranch = "branch" // git 分支（goal/N-* 约定）
	ResultCommit = "commit" // 提交（消息含 Closes #N / goal/N:）
	ResultManual = "manual" // 手动登记的交付物（文件、链接、报告）
)

// Result is something verifiable that was produced for a goal.
// GoalNumber 0 means the result has not been attributed to any goal yet
// (「未归属的结果」, discovered from an unrecognized goal/N branch).
type Result struct {
	ID           string    `json:"id"` // kind:ref, stable within a space
	SpaceID      string    `json:"space_id"`
	GoalNumber   int       `json:"goal_number"`
	Kind         string    `json:"kind"`
	Ref          string    `json:"ref"` // 分支名 / 短 SHA / URL
	Title        string    `json:"title,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// Inbox kinds.
const (
	InboxResult     = "result"     // 某个目标有了新结果
	InboxUnassigned = "unassigned" // git 发现了无法归属的 goal/N 分支
	InboxManual     = "manual"     // 手动放进来的一条
)

// InboxItem is one thing waiting for the user's decision on any surface.
type InboxItem struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Body       string    `json:"body,omitempty"`
	SpaceID    string    `json:"space_id,omitempty"`
	GoalNumber int       `json:"goal_number,omitempty"`
	ResultID   string    `json:"result_id,omitempty"`
	Done       bool      `json:"done"` // 已处理（归档）
	CreatedAt  time.Time `json:"created_at"`
}
