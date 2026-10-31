package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

func (a *preview) renameNativeSession(s *session) {
	a.Live.Rename = true
	a.Live.RenameOwner = s
	a.Live.TitleInput = s.Title
	a.MoreMenu = false
}
func (a *preview) patchSessionPreference(s *session, patch map[string]any, done func()) {
	if s.RemoteID == "" {
		return
	}
	a.operation("preferences/"+s.RemoteID, "PATCH", sessionPath(s.RemoteID), patch, func(any) {
		if title, ok := patch["title"].(string); ok {
			s.Title = title
		}
		if pinned, ok := patch["pinned"].(bool); ok {
			s.Pinned = pinned
		}
		if archived, ok := patch["archived"].(bool); ok {
			s.Archived = archived
			if a.current() == s {
				a.Archived = archived
			}
		}
		a.refreshLiveSessions("")
		if done != nil {
			done()
		}
	})
}
func (a *preview) deleteNativeSession(s *session) {
	a.Live.Confirm = &nativeConfirmation{"删除会话？", s.Title + "；这会永久删除当前主机上的会话记录。", func() {
		a.operation("delete-session", "DELETE", sessionPath(s.RemoteID), nil, func(any) { a.refreshLiveSessions("") })
	}}
}
func (a *preview) forkNativeSession(s *session) { a.forkNativeSessionTo(s, nil) }
func (a *preview) forkNativeSessionTo(s *session, application *string) {
	body := map[string]any{}
	if application != nil {
		body["application"] = *application
	}
	a.operation("fork/"+s.RemoteID, "POST", sessionPath(s.RemoteID)+"/fork", body, func(v any) { a.refreshLiveSessions(str(object(v)["id"])) })
}
func (a *preview) sessionOptions(c *ui.Context, s *session, close func()) {
	busy := s.Running || s.Pending || s.Sending
	pin := "置顶"
	if s.Pinned {
		pin = "取消置顶"
	}
	menuOption(c, "pin", pin, false).Disabled(a.Live.Loading["preferences/"+s.RemoteID]).OnClick(func() { close(); a.patchSessionPreference(s, map[string]any{"pinned": !s.Pinned}, nil) })
	menuOption(c, "git-branch", "分叉会话", false).Disabled(busy || a.Live.Loading["fork/"+s.RemoteID]).OnClick(func() { close(); a.forkNativeSession(s) })
	for _, target := range []struct{ Name, ID string }{{"编程", ""}, {"音乐", "music"}, {"知识库", "kb"}} {
		menuOption(c, "git-branch", "分叉为："+target.Name, false).Disabled(busy || a.Live.Loading["fork/"+s.RemoteID]).OnClick(func() { close(); a.forkNativeSessionTo(s, &target.ID) })
	}
	menuOption(c, "edit", "重命名", false).OnClick(func() { close(); a.renameNativeSession(s) })
	archive := "归档"
	if s.Archived {
		archive = "恢复对话"
	}
	menuOption(c, "archive", archive, false).Disabled(busy || a.Live.Loading["preferences/"+s.RemoteID]).OnClick(func() { close(); a.patchSessionPreference(s, map[string]any{"archived": !s.Archived}, nil) })
	menuOption(c, "delete", "删除", false).Disabled(busy).OnClick(func() { close(); a.deleteNativeSession(s) })
}
func (a *preview) sessionRow(c *ui.Context, s *session) {
	root := ui.Row(c.Key(s.ID)).Gap(1).MinWidth(0).Radius(6)
	open := ui.Local(root, "menu", func() bool { return false })
	if a.Active == s.ID {
		root.Background(c.Theme().Accent.Alpha(.12)).Border(1, c.Theme().Accent.Alpha(.26))
	} else if root.Hovered() {
		root.Background(c.Theme().SurfaceHover)
	}
	root.Children(func() {
		item := ui.ButtonBase(c).Label(s.Title).Grow(1).MinWidth(0).MinHeight(34).Padding(5, 8).Gap(7).OnClick(func() { a.selectSession(s.ID) })
		item.Children(func() {
			ui.Text(c, s.Title).FontSize(13).MaxLines(1).Grow(1).MinWidth(0)
			if s.Pinned {
				icon(c, "pin")
			}
			if s.Unread > 0 {
				ui.Text(c, fmt.Sprint(s.Unread)).Label("未读结果").FontSize(10).Padding(2, 5).Radius(8).Background(c.Theme().Accent.Alpha(.15)).TextColor(c.Theme().Accent)
			}
			if s.Application != "" {
				ui.Text(c, strings.ToUpper(s.Application)).FontSize(9).Padding(2, 5).Radius(8).Background(c.Theme().Accent.Alpha(.12)).TextColor(c.Theme().Accent)
			}
			if s.Running || s.Pending {
				ui.Text(c, "●").TextColor(c.Theme().Accent)
			} else if label := relativeSessionTime(s.UpdatedAt); label != "" {
				ui.Text(c, label).FontSize(10).TextColor(c.Theme().TextMuted)
			}
		})
		trigger := iconButton(c, "menu", "会话操作："+s.Title).Size(24, 28).OnClick(func() { *open = !*open })
		a.menuPanel(c, trigger, open, func() { a.sessionOptions(c, s, func() { *open = false }) })
		item.ContextMenu(func(menu *ui.Menu) {
			if menu.Item("重命名").Chosen() {
				a.renameNativeSession(s)
			}
			label := "置顶"
			if s.Pinned {
				label = "取消置顶"
			}
			if menu.Item(label).Chosen() {
				a.patchSessionPreference(s, map[string]any{"pinned": !s.Pinned}, nil)
			}
			if !s.Running && !s.Pending && !s.Sending {
				if menu.Item("分叉会话").Chosen() {
					a.forkNativeSession(s)
				}
			}
		})
	})
}

// SessionInfo lists activity as Unix seconds; creation responses only carry
// created_at. Normalize both before rendering relative dates or time buckets.
func sessionActivityTime(metadata map[string]any) string {
	for _, field := range []string{"last_active", "created_at"} {
		if seconds := int64(number(metadata[field])); seconds > 0 {
			return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func relativeSessionTime(raw string) string {
	stamp, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return ""
	}
	age := time.Since(stamp)
	if age < time.Minute {
		return "刚刚"
	}
	if age < time.Hour {
		return fmt.Sprintf("%d 分钟前", int(age.Minutes()))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%d 小时前", int(age.Hours()))
	}
	return fmt.Sprintf("%d 天前", int(age.Hours()/24))
}

var sidebarBucketLabels = [3]string{"今天", "7 天内", "更早"}

// sessionBucket groups sidebar sessions by local calendar age: today, the
// previous seven days, or older. Unparsable and future stamps sort with the
// newest and oldest group respectively.
func sessionBucket(raw string) int {
	stamp, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 2
	}
	local := stamp.Local()
	today := time.Now()
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	if !local.Before(today) {
		return 0
	}
	if !local.Before(today.AddDate(0, 0, -6)) {
		return 1
	}
	return 2
}
func (a *preview) saveRename() {
	l := a.Live
	s := l.RenameOwner
	if s == nil {
		s = a.current()
	}
	title := strings.TrimSpace(l.TitleInput)
	if title == "" {
		return
	}
	a.patchSessionPreference(s, map[string]any{"title": title}, func() {
		if l.RenameOwner == s {
			l.Rename = false
			l.RenameOwner = nil
			a.MoreMenu = false
		}
	})
}
