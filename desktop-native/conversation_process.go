package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"strings"
)

type processRange struct {
	Start, End, Answer, Tools int
	Running, Abnormal         bool
}

const processPageSize = 25

// A user input is always a visible boundary. A trailing text becomes an answer
// only after the work stops; an earlier commentary cannot stand in for one.
func conversationProcess(s *session, start int) processRange {
	p := processRange{Start: start, End: start, Answer: -1}
	for p.End < len(s.Messages) && !s.Messages[p.End].User {
		m := s.Messages[p.End]
		if m.Kind == "tool" {
			p.Tools++
		}
		if m.Kind == "error" || m.Status == "失败" || m.Status == "未完成" || m.Status == "已拒绝" {
			p.Abnormal = true
		}
		p.End++
	}
	p.Running = p.End == len(s.Messages) && (s.Running || s.Pending)
	if p.End == len(s.Messages) && s.Run != nil && s.Run.State != "completed" && !s.Run.active() {
		p.Abnormal = true
	}
	if !p.Running && p.End > start {
		last := s.Messages[p.End-1]
		if last.Kind == "" && last.Tool == "" && strings.TrimSpace(last.Text) != "" {
			p.Answer = p.End - 1
		}
	}
	return p
}
func (a *preview) processMessages(c *ui.Context, s *session, p processRange, selected int) {
	end := p.End
	if p.Answer >= 0 {
		end = p.Answer
	}
	if end > p.Start {
		root := ui.Column(c.Key(fmt.Sprintf("process/%s/%d", s.RemoteID, p.Start))).Gap(8).MinWidth(0)
		state := ui.Local(root, "open", func() toolGroupState {
			return toolGroupState{Open: p.Running || p.Abnormal || p.Answer < 0, Running: p.Running, Failed: p.Abnormal, Shown: processPageSize}
		})
		if state.Running && !p.Running && !state.Touched {
			state.Open = p.Abnormal || p.Answer < 0
		}
		if p.Abnormal && !state.Failed {
			state.Open = true
		}
		state.Running = p.Running
		state.Failed = p.Abnormal
		open := state.Open || s.Density == "verbose" || (selected >= p.Start && selected < end)
		label := "工作过程"
		if p.Running {
			label = "正在工作"
		} else if p.Abnormal {
			label = "工作过程 · 有失败或中断"
		}
		label += fmt.Sprintf(" · %d 个步骤 · %d 个工具调用", end-p.Start, p.Tools)
		root.Children(func() {
			glyph := "chevron-right"
			if open {
				glyph = "chevron-down"
			}
			flatButton(c, glyph, label).Label("展开或收起工作过程").Expanded(open).FillWidth().Justify(ui.Start).FontSize(12).OnClick(func() { state.Open = !open; state.Touched = true })
			if open {
				first := max(p.Start, end-max(processPageSize, state.Shown))
				if first > p.Start {
					a.action(c, fmt.Sprintf("显示更早步骤（还有 %d 个）", first-p.Start)).AlignSelf(ui.Start).OnClick(func() { state.Shown = max(processPageSize, state.Shown) + processPageSize })
				}
				// Search can reveal an older match without rendering every step
				// between it and the recent window.
				if selected >= p.Start && selected < first {
					m := s.Messages[selected]
					if m.Kind == "thought" || m.Kind == "tool" {
						m.Expanded = true
					}
					a.conversationRow(c.Key(selected), s, selected, selected)
					a.note(c, "中间步骤已折叠，可继续显示更早步骤。")
				}
				for i := first; i < end; i++ {
					m := s.Messages[i]
					if i == selected && (m.Kind == "thought" || m.Kind == "tool") {
						m.Expanded = true
					}
					a.conversationRow(c.Key(i), s, i, selected)
				}
			}
		})
	}
	for i := p.Start; i < end; i++ {
		if s.Messages[i].Kind == "tool" && s.Messages[i].Tool == "create_workflow" {
			a.conversationWorkflow(c.Key(fmt.Sprintf("workflow/%d", i)), s, s.Messages[i])
		}
	}
	if p.Answer >= 0 {
		a.conversationRow(c.Key(p.Answer), s, p.Answer, selected)
	}
}
