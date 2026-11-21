package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (a *preview) liveWorkflowSettings(c *ui.Context) {
	l := a.Live
	data := object(l.Data["workflows"])
	a.note(c, "在编程会话或管家对话中输入 /workflow 和任务；管家会新建编程会话承接。运行主机需要 Node.js 22.13 或更高版本。运行中的状态每 3 秒更新。")
	if err := str(l.ResourceErrors["workflows"]); err != "" {
		a.note(c, err)
		return
	}
	if !boolean(data["available"]) && str(data["error"]) != "" {
		a.note(c, str(data["error"]))
	}
	runs := list(data["runs"])
	if len(runs) == 0 {
		a.note(c, "还没有工作流运行记录。")
	}
	for _, v := range runs {
		run := object(v)
		id, name, status := str(run["id"]), str(run["name"]), str(run["status"])
		if id == "" {
			continue
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			a.action(c, name+" · "+workflowStatus(status)).OnClick(func() {
				l.WorkflowSelected = id
				a.resource("workflow-detail", "/dynamic-workflows/"+url.PathEscape(id))
			})
			if status == "running" {
				a.action(c, "取消运行").OnClick(func() {
					a.requestConfirmation("取消工作流？", "已完成的操作不会撤销。", func() { a.workflowAction(id, "cancel", nil) })
				})
			}
		})
	}
	if l.WorkflowSelected == "" {
		return
	}
	detail := object(l.Data["workflow-detail"])
	if str(detail["id"]) != l.WorkflowSelected {
		return
	}
	a.heading(c, str(detail["name"])+" · "+workflowStatus(str(detail["status"])))
	if e := str(detail["error"]); e != "" {
		a.note(c, e)
	}
	state := object(detail["state"])
	actorNames := map[string]string{}
	for _, v := range list(state["actors"]) {
		actor := object(v)
		site := str(actor["siteId"])
		name := str(actor["name"])
		if site != "" && name != "" {
			actorNames[site] = name
		}
	}
	for _, v := range list(state["nodes"]) {
		n := object(v)
		label := str(n["siteId"])
		if label == "" {
			label = str(n["site_id"])
		}
		status := str(n["status"])
		if name := actorNames[str(n["actorSiteId"])]; name != "" {
			label = name + " / " + label
		}
		if label != "" || status != "" {
			a.note(c, fmt.Sprintf("%s · %s", label, workflowStatus(status)))
		}
		if result := n["result"]; result != nil {
			b, _ := json.Marshal(result)
			text := string(b)
			if len([]rune(text)) > 500 {
				text = string([]rune(text)[:500]) + "…"
			}
			a.note(c, "结果："+strings.Trim(text, "\""))
		}
		if failure := n["error"]; failure != nil {
			b, _ := json.Marshal(failure)
			a.note(c, "错误："+string(b))
		}
	}
	actors := object(detail["sessions"])
	for name, id := range actors {
		a.note(c, fmt.Sprintf("角色 %s · 会话 %s", name, str(id)))
	}
	if result := detail["result"]; result != nil {
		b, _ := json.Marshal(result)
		a.note(c, "结果："+strings.Trim(string(b), "\""))
	}
	status := str(detail["status"])
	if status == "cancelled" || status == "interrupted" || status == "stopped" {
		a.action(c, "恢复运行").AlignSelf(ui.Start).OnClick(func() {
			id := l.WorkflowSelected
			a.requestConfirmation("恢复工作流？", "未完成步骤可能重复执行写入或命令。请先检查执行记录。", func() { a.workflowAction(id, "resume", map[string]any{"acknowledge_incomplete_actions": true}) })
		})
	}
}
func (a *preview) workflowAction(id, action string, body any) {
	a.operation("workflow-action", "POST", "/dynamic-workflows/"+url.PathEscape(id)+"/"+action, body, func(any) {
		delete(a.Live.Data, "workflow-card/"+id)
		a.resource("workflows", "/dynamic-workflows")
		a.resource("workflow-detail", "/dynamic-workflows/"+url.PathEscape(id))
	})
}
func workflowStatus(v string) string {
	switch v {
	case "running":
		return "运行中"
	case "completed":
		return "已完成"
	case "failed", "errored":
		return "失败"
	case "cancelled", "stopped":
		return "已取消"
	case "interrupted":
		return "已中断"
	case "pending":
		return "等待中"
	default:
		return v
	}
}
