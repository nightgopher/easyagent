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
			return toolGroupState{Open: p.Running || p.Abnormal || p.Answer < 0, Running: p.Running, Failed: p.Abnormal}
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
				for i := p.Start; i < end; i++ {
					m := s.Messages[i]
					if i == selected && (m.Kind == "thought" || m.Kind == "tool") {
						m.Expanded = true
					}
					a.conversationRow(c.Key(i), s, i, selected)
				}
			}
		})
	}
	if p.Answer >= 0 {
		a.conversationRow(c.Key(p.Answer), s, p.Answer, selected)
	}
}
