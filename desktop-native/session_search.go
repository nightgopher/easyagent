package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

const sessionSearchPageSize = 30

type sessionSearchHit struct {
	SessionID  string `json:"session_id"`
	Title      string `json:"title"`
	Workspace  string `json:"workspace"`
	Archived   bool   `json:"archived"`
	UpdatedAt  int64  `json:"updated_at"`
	EntryID    string `json:"entry_id"`
	Role       string `json:"role"`
	Timestamp  int64  `json:"timestamp"`
	Snippet    string `json:"snippet"`
	MatchStart int    `json:"match_start"`
	MatchEnd   int    `json:"match_end"`
}
type sessionSearchResponse struct {
	Hits        []sessionSearchHit `json:"hits"`
	Total       int                `json:"total"`
	Unavailable int                `json:"unavailable_sessions"`
}
type sessionHistoryMessage struct {
	EntryID   string `json:"entry_id"`
	Role      string `json:"role"`
	Timestamp int64  `json:"timestamp"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Document  markdownDocument
}
type sessionHistoryPreview struct {
	SessionID, EntryID, Query, Error string
	Loading, Jump                    bool
	Messages                         []sessionHistoryMessage `json:"messages"`
}
type nativeSessionSearch struct {
	Query, Workspace, Archive, Period, Error string
	Due                                      time.Time
	Cancel                                   context.CancelFunc
	Version                                  uint64
	Loading, Loaded, Focus, Composing        bool
	Offset                                   int
	Result                                   sessionSearchResponse
	Target                                   *sessionSearchHit
	TargetQuery                              string
	History                                  *sessionHistoryPreview
}

func (a *preview) resetSessionSearch() {
	s := &a.Live.SessionSearch
	if s.Cancel != nil {
		s.Cancel()
	}
	*s = nativeSessionSearch{Version: s.Version + 1}
}

func (a *preview) queueSessionSearch() {
	s := &a.Live.SessionSearch
	if s.Cancel != nil {
		s.Cancel()
		s.Cancel = nil
	}
	s.Version++
	s.Query = strings.TrimSpace(a.Search)
	s.Result = sessionSearchResponse{}
	s.Offset, s.Error, s.Loading, s.Loaded = 0, "", false, false
	s.Due = time.Time{}
	if s.Query != "" {
		s.Due = time.Now().Add(250 * time.Millisecond)
	}
}

func (a *preview) tickSessionSearch() bool {
	s := &a.Live.SessionSearch
	if s.Composing {
		return false
	}
	if s.Query != strings.TrimSpace(a.Search) {
		a.queueSessionSearch()
		return true
	}
	if !s.Due.IsZero() && !time.Now().Before(s.Due) {
		s.Due = time.Time{}
		a.searchSessionPage(0)
		return true
	}
	return false
}

func (a *preview) searchSessionPage(offset int) {
	l, s := a.Live, &a.Live.SessionSearch
	if s.Cancel != nil {
		s.Cancel()
	}
	s.Version++
	version, epoch, b := s.Version, l.epoch, l.Client
	s.Due, s.Error = time.Time{}, ""
	if s.Query == "" {
		return
	}
	if utf8.RuneCountInString(s.Query) > 256 {
		s.Error = "搜索内容最多 256 个字"
		return
	}
	if !l.Connected || b == nil {
		s.Error = "连接当前主机后即可搜索历史会话"
		return
	}
	values := map[string]string{"q": s.Query, "limit": strconv.Itoa(sessionSearchPageSize), "offset": strconv.Itoa(offset)}
	if s.Workspace != "全部项目" {
		values["workspace"] = s.Workspace
	}
	values["archived"] = map[string]string{"最近会话": "false", "已归档": "true"}[s.Archive]
	if days := map[string]int{"最近 7 天": 7, "最近 30 天": 30}[s.Period]; days > 0 {
		values["since"] = strconv.FormatInt(time.Now().AddDate(0, 0, -days).UnixMilli(), 10)
	}
	parent := l.hostCtx
	if parent == nil {
		parent = l.ctx
	}
	ctx, cancel := context.WithCancel(parent)
	s.Cancel = cancel
	s.Loading = true
	path := "/sessions/search" + query(values)
	go func() {
		defer cancel()
		var result sessionSearchResponse
		err := b.request(ctx, "GET", path, nil, &result)
		a.post(epoch, func() {
			if s.Version != version {
				return
			}
			s.Loading, s.Loaded, s.Cancel = false, true, nil
			if err != nil {
				s.Result = sessionSearchResponse{}
				s.Error = "搜索失败：" + err.Error()
				if strings.Contains(err.Error(), "HTTP 404") {
					s.Error = "当前主机尚不支持全文搜索，请升级该主机上的 EasyAgent"
				}
				if errors.Is(err, errUnauthorized) {
					a.reportLiveError(err)
				}
				return
			}
			s.Result, s.Offset = result, offset
		})
	}()
}

func (a *preview) sessionSearchControls(c *ui.Context) {
	s := &a.Live.SessionSearch
	if s.Workspace == "" {
		s.Workspace = "全部项目"
	}
	if s.Archive == "" {
		s.Archive = "全部会话"
	}
	if s.Period == "" {
		s.Period = "不限时间"
	}
	name := localHostName()
	for _, h := range a.Live.Config.Hosts {
		if h.ID == a.Live.Config.ActiveHost {
			name = h.Name
		}
	}
	ui.Text(c, "搜索范围："+name).FontSize(11).TextColor(c.Theme().TextMuted).MaxLines(1).Tooltip("搜索此主机保存的标题、提问和回答，默认包含已归档会话")
	projects := []string{"全部项目"}
	seen := map[string]bool{}
	for _, session := range a.Sessions {
		if session.Workspace != "" && !seen[session.Workspace] {
			seen[session.Workspace] = true
			projects = append(projects, session.Workspace)
		}
	}
	sort.Strings(projects[1:])
	if a.selectControl(c, "搜索项目", &s.Workspace, projects).FillWidth().MinWidth(0).Changed() {
		a.queueSessionSearch()
	}
	ui.Row(c).Gap(6).Children(func() {
		if a.selectControl(c, "搜索归档范围", &s.Archive, []string{"全部会话", "最近会话", "已归档"}).Grow(1).MinWidth(0).Changed() {
			a.queueSessionSearch()
		}
		if a.selectControl(c, "搜索时间范围", &s.Period, []string{"不限时间", "最近 7 天", "最近 30 天"}).Grow(1).MinWidth(0).Changed() {
			a.queueSessionSearch()
		}
	})
}

func searchHighlighted(c *ui.Context, text string, start, end int) ui.Element {
	runes := []rune(text)
	start, end = max(0, min(start, len(runes))), max(0, min(end, len(runes)))
	if end <= start {
		return ui.Text(c, text)
	}
	return ui.RichText(c, ui.Span{Text: string(runes[:start])}, ui.Span{Text: string(runes[start:end]), Weight: 600, Background: c.Theme().Accent.Alpha(.18)}, ui.Span{Text: string(runes[end:])})
}

func (a *preview) sessionSearchResults(c *ui.Context) {
	s := &a.Live.SessionSearch
	ui.Column(c).Gap(5).MinWidth(0).Children(func() {
		switch {
		case s.Error != "":
			ui.Text(c, s.Error).FontSize(12).TextColor(c.Theme().Danger).Padding(8).Role(ui.RoleStatus)
			flatButton(c, "refresh", "重新搜索").AlignSelf(ui.Start).OnClick(func() { a.searchSessionPage(0) })
			return
		case s.Loading || !s.Loaded:
			ui.Text(c, "正在搜索历史会话…").FontSize(12).TextColor(c.Theme().TextMuted).Padding(8).Role(ui.RoleStatus)
			ui.Text(c, "首次搜索会建立索引，之后自动更新。").FontSize(11).TextColor(c.Theme().TextMuted).Padding(0, 8)
			return
		}
		if s.Result.Unavailable > 0 {
			ui.Text(c, fmt.Sprintf("%d 个会话暂时无法读取，结果可能不完整", s.Result.Unavailable)).FontSize(11).TextColor(c.Theme().Danger).Padding(8)
		}
		if s.Result.Total == 0 {
			ui.Text(c, "没有匹配的会话内容").FontSize(13).Padding(8, 8, 0, 8)
			ui.Text(c, "试试对话中的短句、文件名或报错，也可以扩大项目和时间范围。").FontSize(12).TextColor(c.Theme().TextMuted).Padding(4, 8)
			return
		}
		ui.Text(c, fmt.Sprintf("%d 条匹配", s.Result.Total)).FontSize(11).TextColor(c.Theme().TextMuted).Padding(6, 8)
		for _, hit := range s.Result.Hits {
			hit := hit
			row := ui.ButtonBase(c.Key(hit.SessionID+"/"+hit.EntryID)).Label("打开搜索结果："+hit.Title+" / "+hit.EntryID).FillWidth().MinWidth(0).Padding(10, 8).Radius(6).Disabled(!a.Live.Connected).OnClick(func() { a.openSessionSearchHit(hit) })
			if row.Hovered() {
				row.Background(c.Theme().SurfaceHover)
			}
			row.Children(func() {
				ui.Column(c).Gap(5).MinWidth(0).Grow(1).Children(func() {
					ui.Text(c, hit.Title).FontSize(13).FontWeight(600).MaxLines(1).MinWidth(0)
					searchHighlighted(c, strings.ReplaceAll(hit.Snippet, "\n", " "), hit.MatchStart, hit.MatchEnd).FontSize(12).LineHeight(1.5).MaxLines(3).MinWidth(0)
					role := map[string]string{"title": "标题", "user": "你", "assistant": "回答"}[hit.Role]
					project := filepath.Base(hit.Workspace)
					if hit.Workspace == "" {
						project = "未分组"
					}
					meta := project + " · " + role
					if hit.Archived {
						meta += " · 已归档"
					}
					ui.Text(c, meta).FontSize(10).TextColor(c.Theme().TextMuted).MaxLines(1).Tooltip(hit.Workspace)
					ui.Text(c, time.UnixMilli(hit.UpdatedAt).Local().Format("2006-01-02 15:04")).FontSize(10).TextColor(c.Theme().TextMuted)
				})
			})
		}
		if s.Result.Total > sessionSearchPageSize {
			ui.Row(c).Padding(8, 0).Gap(4).AlignItems(ui.Center).Children(func() {
				iconButton(c, "arrow-left", "上一页搜索结果").Disabled(s.Offset == 0).OnClick(func() { a.searchSessionPage(max(0, s.Offset-sessionSearchPageSize)) })
				ui.Text(c, fmt.Sprintf("%d–%d / %d", s.Offset+1, s.Offset+len(s.Result.Hits), s.Result.Total)).FontSize(11).Grow(1).TextAlign(ui.Center)
				iconButton(c, "chevron-right", "下一页搜索结果").Disabled(s.Offset+len(s.Result.Hits) >= s.Result.Total).OnClick(func() { a.searchSessionPage(s.Offset + sessionSearchPageSize) })
			})
		}
	})
}

func (a *preview) openSessionSearchHit(hit sessionSearchHit) {
	var target *session
	for _, candidate := range a.Sessions {
		if candidate.RemoteID == hit.SessionID {
			target = candidate
			break
		}
	}
	if target == nil {
		target = &session{ID: a.NextID, RemoteID: hit.SessionID, Title: hit.Title, Workspace: hit.Workspace, Project: filepath.Base(hit.Workspace), Archived: hit.Archived}
		a.NextID++
		a.Sessions = append(a.Sessions, target)
	}
	a.selectSession(target.ID)
	a.Archived = hit.Archived
	if hit.EntryID == "" {
		return
	}
	s := &a.Live.SessionSearch
	s.Target, s.TargetQuery = &hit, s.Query
	// Resolve against the fresh snapshot; cached messages may predate a compact.
}

func (a *preview) resolveSessionSearchHit(s *session) {
	search := &a.Live.SessionSearch
	hit := search.Target
	if hit == nil || hit.SessionID != s.RemoteID || a.current() != s {
		return
	}
	search.Target = nil
	matches := conversationMatches(s.Messages, search.TargetQuery)
	for index, match := range matches {
		m := s.Messages[match.Index]
		if m.EntryID == hit.EntryID && m.Tool == "" && m.Kind == "" {
			s.FindOpen, s.FindJump, s.FindFocus = true, true, false
			s.FindQuery, s.FindIndex = search.TargetQuery, index
			return
		}
	}
	a.openSearchHistory(*hit, search.TargetQuery)
}

func (a *preview) openSearchHistory(hit sessionSearchHit, q string) {
	l := a.Live
	preview := &sessionHistoryPreview{SessionID: hit.SessionID, EntryID: hit.EntryID, Query: q, Loading: true, Jump: true}
	l.SessionSearch.History = preview
	epoch, b, ctx := l.epoch, l.Client, l.hostCtx
	if ctx == nil {
		ctx = l.ctx
	}
	go func() {
		var response sessionHistoryPreview
		err := b.request(ctx, "GET", sessionPath(hit.SessionID)+"/history"+query(map[string]string{"entry_id": hit.EntryID}), nil, &response)
		a.post(epoch, func() {
			if l.SessionSearch.History != preview {
				return
			}
			preview.Loading = false
			if err != nil {
				preview.Error = "无法读取历史片段：" + err.Error()
				return
			}
			preview.Messages = response.Messages
		})
	}()
}

func (a *preview) searchHistoryTranscript(c *ui.Context) {
	h := a.Live.SessionSearch.History
	ui.Row(c).Gap(8).Padding(10, 20).Background(c.Theme().Surface).Children(func() {
		ui.Text(c, "历史片段 · 只读").FontSize(13).Grow(1)
		flatButton(c, "arrow-left", "返回当前对话").OnClick(func() { a.Live.SessionSearch.History = nil })
	})
	ui.Scroll(c.Key("search-history/" + h.SessionID + "/" + h.EntryID)).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().MaxWidth(816).MinWidth(0).Margin(0, ui.Auto).Padding(20).Gap(18).Children(func() {
			ui.Text(c, "原始消息可能已被压缩，或位于另一条历史分支。查看这里不会改变当前对话的上下文。").FontSize(12).TextColor(c.Theme().TextMuted)
			if h.Loading {
				ui.Text(c, "正在读取原始消息…").Role(ui.RoleStatus)
				return
			}
			if h.Error != "" {
				ui.Text(c, h.Error).TextColor(c.Theme().Danger).Role(ui.RoleStatus)
				return
			}
			for i := range h.Messages {
				m := &h.Messages[i]
				row := ui.Column(c.Key(m.EntryID)).MinWidth(0).Gap(8).Padding(12).Radius(6)
				if m.EntryID == h.EntryID {
					row.Background(c.Theme().Accent.Alpha(.07)).Label("历史搜索命中")
					if h.Jump {
						row.ScrollIntoView()
						h.Jump = false
					}
				}
				row.Children(func() {
					role := "EasyAgent"
					if m.Role == "user" {
						role = "你"
					}
					ui.Text(c, role+" · "+time.UnixMilli(m.Timestamp).Local().Format("2006-01-02 15:04")).FontSize(12).TextColor(c.Theme().TextMuted)
					if largeRichText(m.Text) {
						m.Document.render(c, m.Text)
					} else {
						matches := conversationMatches([]*message{{Text: m.Text}}, h.Query)
						start, end := 0, 0
						if len(matches) > 0 {
							start = matches[0].Offset
							end = start + utf8.RuneCountInString(h.Query)
						}
						searchHighlighted(c, m.Text, start, end).FontSize(14).LineHeight(1.7).Selectable().MinWidth(0)
					}
					if m.Truncated {
						ui.Text(c, "相邻消息较长，仅显示开头；可搜索这条消息中的文字查看原文。").FontSize(11).TextColor(c.Theme().TextMuted)
					}
				})
			}
		})
	})
	if c.Shortcut(0, ui.KeyEscape) {
		a.Live.SessionSearch.History = nil
	}
}
