package command

import (
	"fmt"
	"strings"
)

// Kickoff builds the「开工包」for a goal: the bounded context handed to
// whichever tool starts the work (Codex desktop, GPT web, EasyAgent itself).
// It deliberately carries no conversation history — goal, definition of done,
// result summaries and the branch convention (docs/decisions/personal-command-center.md).
func (s *Store) Kickoff(goalNumber int) (string, error) {
	goal, err := s.Goal(goalNumber)
	if err != nil {
		return "", err
	}
	space, err := s.Space(goal.SpaceID)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# 开工包：目标 #%d %s\n\n", goal.Number, goal.Title)

	b.WriteString("## 目标\n")
	if goal.Description != "" {
		b.WriteString(goal.Description)
	} else {
		b.WriteString(goal.Title)
	}
	b.WriteString("\n\n")

	b.WriteString("## 完成标准\n")
	if len(goal.Done) == 0 {
		b.WriteString("- （未填写，补全后再开工更稳）\n")
	}
	for _, d := range goal.Done {
		box := "[ ]"
		if d.Check {
			box = "[x]"
		}
		fmt.Fprintf(&b, "- %s %s\n", box, d.Text)
	}
	b.WriteString("\n")

	results := s.Results(goal.SpaceID, goal.Number)
	if len(results) > 0 {
		b.WriteString("## 已有结果\n")
		limit := len(results)
		if limit > 5 {
			limit = 5
		}
		for _, r := range results[:limit] {
			fmt.Fprintf(&b, "- [%s] %s\n", r.Kind, r.Ref)
		}
		if len(results) > limit {
			fmt.Fprintf(&b, "- …另有 %d 条\n", len(results)-limit)
		}
		b.WriteString("\n")
	}

	if space.Notes != "" {
		b.WriteString("## 空间笔记\n")
		b.WriteString(space.Notes)
		b.WriteString("\n\n")
	}

	b.WriteString("## 仓库\n")
	if space.RepoPath != "" {
		fmt.Fprintf(&b, "%s（绝对路径，直接在这里工作）\n\n", space.RepoPath)
	} else {
		b.WriteString("（空间未关联仓库；若需要改代码，先在指挥台关联）\n\n")
	}

	b.WriteString("## 分支约定\n")
	fmt.Fprintf(&b, "- 分支名：goal/%d-<短横线描述>\n", goal.Number)
	fmt.Fprintf(&b, "- 提交信息：goal/%d: <说明>\n", goal.Number)
	fmt.Fprintf(&b, "- 完成时在提交信息或 PR 描述中写 Closes #%d\n\n", goal.Number)

	b.WriteString("## 约束\n")
	b.WriteString("- 只做与该目标直接相关的修改\n")
	b.WriteString("- 不推送远端时保持本地提交\n")
	b.WriteString("- 达成完成标准即停止\n")

	return b.String(), nil
}
