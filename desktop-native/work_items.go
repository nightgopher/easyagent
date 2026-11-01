package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

type nativeWorkArtifact struct {
	Title string `json:"title"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type nativeWorkItem struct {
	ID             string               `json:"id"`
	SessionID      string               `json:"session_id"`
	AgentID        string               `json:"agent_id"`
	Application    string               `json:"application"`
	Workspace      string               `json:"workspace"`
	Title          string               `json:"title"`
	State          string               `json:"state"`
	Summary        string               `json:"summary"`
	Question       string               `json:"question"`
	Artifacts      []nativeWorkArtifact `json:"artifacts"`
	Version        int64                `json:"version"`
	UpdatedAt      string               `json:"updated_at"`
	RunState       string               `json:"run_state"`
	RunError       string               `json:"run_error"`
	Attention      string               `json:"attention"`
	SessionMissing bool                 `json:"session_missing"`
}
type nativeWorkPage struct {
	Items   []nativeWorkItem `json:"items"`
	HasMore bool             `json:"has_more"`
}
type nativeWorkState struct {
	Open                                bool
	View, Query, AppliedQuery, Expanded string
	Offset                              int
	NextPoll                            time.Time
	Cancel                              context.CancelFunc
}

func (a *preview) cancelWorkRead() {
	l := a.Live
	l.Versions["work"]++
	if l.Work.Cancel != nil {
		l.Work.Cancel()
		l.Work.Cancel = nil
	}
	l.Loading["work"] = false
}
func (a *preview) closeWork() {
	if a.Live == nil {
		return
	}
	a.cancelWorkRead()
	a.Live.Work.Open = false
}
func (a *preview) openWork() {
	l := a.Live
	l.Work.Open = true
	if l.Work.View == "" {
		l.Work.View = "attention"
	}
	a.Workbench = false
	if l.Connected && l.Data["business-agents"] == nil {
		a.resource("business-agents", "/agents")
	}
	a.loadWorkPage(false)
}
func (a *preview) tickWork() {
	l := a.Live
	if !l.Work.Open || a.Settings.Open || !l.Connected || l.Client == nil || l.Loading["work-save"] {
		if l.Work.Cancel != nil {
			a.cancelWorkRead()
		}
		return
	}
	if l.Work.Cancel == nil && time.Now().After(l.Work.NextPoll) {
		a.loadWorkPage(true)
	}
}
func (a *preview) loadWorkPage(background bool) {
	l, s := a.Live, &a.Live.Work
	a.cancelWorkRead()
	if l.Client == nil || !l.Connected {
		l.ResourceErrors["work"] = "连接当前主机后即可查看任务"
		return
	}
	if !background {
		delete(l.Data, "work")
		delete(l.ResourceErrors, "work")
		l.Loading["work"] = true
	}
	ctx, cancel := context.WithTimeout(l.hostCtx, 10*time.Second)
	s.Cancel = cancel
	version, epoch, b := l.Versions["work"], l.epoch, l.Client
	path := "/work" + query(map[string]string{"view": s.View, "q": s.AppliedQuery, "offset": strconv.Itoa(s.Offset), "limit": "30"})
	go func() {
		defer cancel()
		var v any
		err := b.request(ctx, http.MethodGet, path, nil, &v)
		a.post(epoch, func() {
			if version != l.Versions["work"] {
				return
			}
			s.Cancel = nil
			l.Loading["work"] = false
			s.NextPoll = time.Now().Add(5 * time.Second)
			if err != nil {
				l.ResourceErrors["work"] = err.Error()
				return
			}
			delete(l.ResourceErrors, "work")
			l.Data["work"] = v
		})
	}()
}
func (a *preview) trackSession(s *session) {
	if s.RemoteID == "" || !a.Live.Connected {
		return
	}
	a.cancelWorkRead()
	a.operation("work-save", "POST", "/work", map[string]any{"session_id": s.RemoteID, "title": s.Title}, func(v any) {
		l := a.Live
		l.Work.View, l.Work.Offset, l.Work.Query, l.Work.AppliedQuery = "open", 0, "", ""
		l.Work.Expanded = str(object(v)["id"])
		l.Notice = "已加入任务跟进；会话内容和草稿已保留。"
		a.openWork()
	})
}
func (a *preview) updateWorkState(item nativeWorkItem, state string) {
	a.cancelWorkRead()
	question := item.Question
	if state != "waiting_answer" && state != "blocked" {
		question = ""
	}
	body := map[string]any{"title": item.Title, "state": state, "summary": item.Summary, "question": question, "artifacts": item.Artifacts, "version": item.Version}
	a.operation("work-save", "PUT", "/work/"+pathSegment(item.ID), body, func(any) { a.loadWorkPage(false) })
}
func (a *preview) openWorkSession(item nativeWorkItem) {
	if item.SessionMissing {
		return
	}
	a.closeWork()
	for _, s := range a.Sessions {
		if s.RemoteID == item.SessionID {
			a.Archived = s.Archived
			a.selectSession(s.ID)
			return
		}
	}
	a.refreshLiveSessions(item.SessionID)
}
func workLabel(item nativeWorkItem) string {
	if item.State == "completed" {
		return "已完成"
	}
	if item.State == "cancelled" {
		return "已结束跟进"
	}
	if label := map[string]string{"confirmation": "等待你确认", "failed": "执行失败", "interrupted": "运行已中断", "cancelled": "运行已取消", "session_missing": "原会话已删除", "waiting_answer": "等待你回答", "waiting_review": "等待你验收", "blocked": "需要你处理"}[item.Attention]; label != "" {
		return label
	}
	if item.RunState == "running" {
		return "正在处理"
	}
	return "待继续"
}
func (a *preview) workItemsView(c *ui.Context) {
	l, s := a.Live, &a.Live.Work
	ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).Padding(24).Gap(16).Children(func() {
		ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
			a.text(c, "任务跟进").FontSize(20).FontWeight(600).Grow(1)
			a.action(c, "刷新任务").Disabled(l.Loading["work"] || l.Loading["work-save"]).OnClick(func() { a.loadWorkPage(false) })
			a.action(c, "返回对话").OnClick(a.closeWork)
		})
		a.note(c, a.Settings.host().Name+" · 查看需要你处理的事，回到原 Agent 继续。")
		ui.Row(c).Wrap().Gap(8).Children(func() {
			for _, choice := range []businessChoice{{"attention", "待处理"}, {"open", "跟进中"}, {"all", "全部任务"}} {
				button := a.action(c, choice.Label).OnClick(func() { s.View, s.Offset = choice.ID, 0; a.loadWorkPage(false) })
				if s.View == choice.ID {
					button.TextColor(c.Theme().Accent).Background(c.Theme().Accent.Alpha(.1))
				}
			}
		})
		ui.Row(c).Gap(8).Children(func() {
			search := func() { s.AppliedQuery, s.Offset = strings.TrimSpace(s.Query), 0; a.loadWorkPage(false) }
			ui.TextInput(c, &s.Query).Label("搜索任务或成果").Placeholder("搜索任务名称、进展或成果…").Grow(1).MinWidth(0).OnSubmit(search)
			a.action(c, "搜索任务").OnClick(search)
		})
		if err := l.ResourceErrors["work-save"]; err != "" {
			ui.Text(c, err+"；请刷新后重试。").FontSize(12).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
		}
		if a.resourceFeedback(c, "work", func() { a.loadWorkPage(false) }) {
			return
		}
		page := businessDecode[nativeWorkPage](l.Data["work"])
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			if len(page.Items) == 0 {
				ui.Column(c).Padding(24, 0).Gap(10).Children(func() {
					if s.View == "attention" && s.AppliedQuery == "" {
						a.text(c, "当前没有需要你处理的事项").FontSize(16)
					} else {
						a.text(c, "没有匹配的任务").FontSize(16)
					}
					a.note(c, "在会话菜单选择「加入任务跟进」，即可持续记录进展和成果。")
					a.action(c, "跟进当前会话").AlignSelf(ui.Start).Disabled(a.current().RemoteID == "" || !l.Connected || l.Loading["work-save"]).OnClick(func() { a.trackSession(a.current()) })
				})
			}
			for _, item := range page.Items {
				a.workItemRow(c, item)
			}
		})
		if s.Offset > 0 || page.HasMore {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				a.action(c, "上一页任务").Disabled(s.Offset == 0 || l.Loading["work"]).OnClick(func() { s.Offset = max(0, s.Offset-30); a.loadWorkPage(false) })
				a.note(c, fmt.Sprintf("第 %d 页", s.Offset/30+1))
				a.action(c, "下一页任务").Disabled(!page.HasMore || l.Loading["work"]).OnClick(func() { s.Offset += 30; a.loadWorkPage(false) })
			})
		}
	})
}
func (a *preview) workItemRow(c *ui.Context, item nativeWorkItem) {
	l, s := a.Live, &a.Live.Work
	ui.Column(c.Key(item.ID)).MinWidth(0).Padding(16, 0).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			glyph := "chevron-right"
			if s.Expanded == item.ID {
				glyph = "chevron-down"
			}
			button := ui.ButtonBase(c).Label("任务："+item.Title).Grow(1).MinWidth(0).Padding(4, 0).Gap(8).OnClick(func() {
				if s.Expanded == item.ID {
					s.Expanded = ""
				} else {
					s.Expanded = item.ID
				}
			})
			button.Children(func() {
				icon(c, glyph)
				ui.Text(c, item.Title).FontSize(14).FontWeight(600).MaxLines(2).Grow(1).MinWidth(0)
			})
			a.businessBadge(c, workLabel(item))
		})
		owner := item.Application
		if owner == "" {
			owner = "编程"
		}
		if item.AgentID != "" {
			owner = a.businessAgentName(item.AgentID)
		}
		a.note(c, owner+" · "+filepath.Base(item.Workspace))
		if item.Summary != "" && s.Expanded != item.ID {
			ui.Text(c, item.Summary).FontSize(13).MaxLines(2).TextColor(c.Theme().TextMuted)
		}
		if item.Question != "" {
			question := ui.Text(c, item.Question).FontSize(13)
			if s.Expanded != item.ID {
				question.MaxLines(3)
			}
		}
		if item.RunError != "" && item.Attention != "" {
			failure := ui.Text(c, item.RunError).FontSize(12).TextColor(c.Theme().Danger)
			if s.Expanded != item.ID {
				failure.MaxLines(3)
			}
		}
		ui.Row(c).Wrap().Gap(8).Children(func() {
			a.action(c, "回到会话").Label("回到会话：" + item.Title).Disabled(item.SessionMissing || !l.Connected).OnClick(func() { a.openWorkSession(item) })
			if item.State != "completed" && item.State != "cancelled" {
				a.action(c, "标记完成").Label("标记完成：" + item.Title).Disabled(l.Loading["work-save"] || !l.Connected || item.RunState == "running" || item.RunState == "waiting_confirmation").OnClick(func() { a.updateWorkState(item, "completed") })
			} else {
				a.action(c, "重新跟进").Label("重新跟进：" + item.Title).Disabled(l.Loading["work-save"] || !l.Connected).OnClick(func() { a.updateWorkState(item, "active") })
			}
		})
		if s.Expanded != item.ID {
			return
		}
		if item.Summary != "" {
			ui.Text(c, item.Summary).FontSize(13).LineHeight(1.6)
		}
		for _, artifact := range item.Artifacts {
			ui.Column(c.Key(artifact.Value)).Gap(5).Padding(8, 0).Children(func() {
				ui.Text(c, artifact.Title).FontSize(13).FontWeight(600)
				ui.Text(c, artifact.Value).FontSize(12).MaxLines(3).TextColor(c.Theme().TextMuted)
				a.action(c, "复制成果位置").Label("复制成果：" + artifact.Title).AlignSelf(ui.Start).OnClick(func() {
					value := artifact.Value
					if artifact.Kind == "file" {
						value = filepath.Join(item.Workspace, artifact.Value)
					}
					c.WriteClipboard(value)
					c.Toast("已复制成果位置")
				})
			})
		}
		if len(item.Artifacts) == 0 {
			a.note(c, "尚未登记成果。可让原 Agent 更新任务进展并记录文件或链接。")
		}
		if item.State != "completed" && item.State != "cancelled" {
			a.action(c, "结束跟进").AlignSelf(ui.Start).Disabled(l.Loading["work-save"] || !l.Connected || item.RunState == "running" || item.RunState == "waiting_confirmation").OnClick(func() { a.updateWorkState(item, "cancelled") })
		}
	})
}
