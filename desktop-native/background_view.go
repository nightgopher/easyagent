package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// backgroundRefresh is how often the 「后台」 page reloads while a task runs.
const backgroundRefresh = 3 * time.Second

func backgroundKey(s *session) string { return "background/" + s.RemoteID }

// loadBackgroundTasks fetches the session's background bash tasks.
func (a *preview) loadBackgroundTasks(s *session) {
	if s.RemoteID == "" || a.Live.Client == nil {
		return
	}
	a.resource(backgroundKey(s), sessionPath(s.RemoteID)+"/background-tasks")
	if id := a.Live.Workbench.BackgroundTask; id != "" {
		a.resource(backgroundKey(s)+"/output", sessionPath(s.RemoteID)+"/background-tasks/"+id+"?tail=16384")
	}
	a.Live.Workbench.BackgroundFetched = time.Now()
}

func (a *preview) backgroundTasks(s *session) []map[string]any {
	var out []map[string]any
	for _, raw := range list(object(a.Live.Data[backgroundKey(s)])["tasks"]) {
		out = append(out, object(raw))
	}
	return out
}

func (a *preview) runningBackgroundTasks(s *session) int {
	n := 0
	for _, task := range a.backgroundTasks(s) {
		if task["running"] == true {
			n++
		}
	}
	return n
}

// backgroundWorkbench lists the session's background tasks with their status,
// lets the user read recent output and stop a running task (#244).
func (a *preview) backgroundWorkbench(c *ui.Context) {
	l := a.Live
	w := &l.Workbench
	s := a.current()
	tasks := a.backgroundTasks(s)
	running := a.runningBackgroundTasks(s)
	if running > 0 && !l.Loading[backgroundKey(s)] && time.Since(w.BackgroundFetched) >= backgroundRefresh {
		a.loadBackgroundTasks(s)
	}
	if running > 0 {
		c.After(backgroundRefresh)
	}
	ui.Scroll(c).Grow(1).MinHeight(0).Padding(12).Children(func() {
		ui.Column(c).Gap(10).Children(func() {
			if err := l.ResourceErrors[backgroundKey(s)]; err != "" {
				a.note(c, err)
			}
			if len(tasks) == 0 {
				a.note(c, "本会话没有后台任务。bash 以 run_in_background 启动的命令会出现在这里。")
				return
			}
			for _, task := range tasks {
				id := str(task["id"])
				ui.Column(c.Key(id)).Gap(6).Padding(10).Radius(6).Border(1, c.Theme().Border).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						dot := c.Theme().TextMuted
						if task["running"] == true {
							dot = c.Theme().Accent
						}
						ui.Text(c, "●").FontSize(9).TextColor(dot)
						ui.Text(c, id).Font("monospace").FontSize(12).FontWeight(500)
						ui.Text(c, str(task["status"])+backgroundElapsed(task)).FontSize(11).TextColor(c.Theme().TextMuted).Grow(1).MinWidth(0).SingleLine()
						a.action(c, "输出").Label("查看输出：" + id).OnClick(func() {
							w.BackgroundTask = id
							a.loadBackgroundTasks(s)
						})
						if task["running"] == true {
							a.action(c, "停止").Label("停止后台任务：" + id).Disabled(l.Loading["background-stop/"+id]).OnClick(func() {
								a.operation("background-stop/"+id, "POST", sessionPath(s.RemoteID)+"/background-tasks/"+id+"/stop", map[string]any{}, func(any) { a.loadBackgroundTasks(s) })
							})
						}
					})
					ui.Text(c, str(task["command"])).Font("monospace").FontSize(11).TextColor(c.Theme().TextMuted).MaxLines(2).Selectable()
					if w.BackgroundTask == id {
						output := str(object(l.Data[backgroundKey(s)+"/output"])["output"])
						if output == "" {
							output = "（暂无输出）"
						}
						nativePre(c, ansiSequence.ReplaceAllString(output, ""))
					}
				})
			}
		})
	})
}

func backgroundElapsed(task map[string]any) string {
	started, err := time.Parse(time.RFC3339Nano, str(task["started"]))
	if err != nil {
		return ""
	}
	end := time.Now()
	if finished, err := time.Parse(time.RFC3339Nano, str(task["finished"])); err == nil && !finished.IsZero() && finished.After(started) {
		end = finished
	}
	return fmt.Sprintf(" · %s", end.Sub(started).Round(time.Second))
}
