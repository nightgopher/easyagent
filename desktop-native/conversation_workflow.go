package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

var workflowIDPattern = regexp.MustCompile(`^dw-[a-f0-9]{24}$`)

// The tool's live progress and durable result both carry the run id. Never
// guess a run from its title or parent: one conversation can start many runs.
func conversationWorkflowID(m *message) string {
	if workflowIDPattern.MatchString(m.WorkflowID) {
		return m.WorkflowID
	}
	if id := str(m.Details["workflow_run_id"]); workflowIDPattern.MatchString(id) {
		return id
	}
	var result struct {
		RunID string `json:"run_id"`
	}
	if json.Unmarshal([]byte(m.Result), &result) == nil && workflowIDPattern.MatchString(result.RunID) {
		return result.RunID
	}
	fields := strings.Fields(m.Result)
	if len(fields) >= 3 && fields[0] == "工作流" && fields[2] == "·" && workflowIDPattern.MatchString(fields[1]) {
		return fields[1]
	}
	return ""
}

func (a *preview) tickConversationWorkflows() {
	l := a.Live
	if l == nil || !l.Connected || l.Client == nil || a.Settings.Open || time.Now().Before(l.WorkflowNext) {
		return
	}
	l.WorkflowNext = time.Now().Add(3 * time.Second)
	seen := map[string]bool{}
	requested := 0
	for _, m := range a.current().Messages {
		if m.Kind != "tool" || m.Tool != "create_workflow" {
			continue
		}
		id := conversationWorkflowID(m)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		key := "workflow-card/" + id
		cached := object(l.Data[key])
		// Finished records are immutable until an explicit resume; refreshing an
		// old conversation should not poll all historical runs forever.
		if l.Loading[key] || (m.Status != "执行中" && len(cached) > 0 && str(cached["status"]) != "running") {
			continue
		}
		path := "/dynamic-workflows/" + url.PathEscape(id)
		if cached["graph"] != nil {
			path += "?omit_graph=1"
		}
		a.resource(key, path)
		requested++
		if requested == 4 {
			break
		}
	}
}

type workflowStage struct {
	ID, Name, Status string
	Actors           []workflowActor
}
type workflowActor struct {
	Name, SessionID, Status, SiteID string
	Ordinal                         float64
}
type workflowRelation struct{ From, To, FromID, ToID, Certainty string }

type workflowNodeKey struct {
	Site, Actor string
	Ordinal     float64
}

func boundedWorkflowList(items []any, limit int) []any { return items[:min(len(items), limit)] }

// Stages are derived only from the engine's graph and journal, never from a
// fabricated order. Older runs without a graph retain their real actor list.
func projectWorkflow(run map[string]any) ([]workflowStage, []workflowRelation) {
	graph, state := object(run["graph"]), object(run["state"])
	steps, nodes, actorRows := boundedWorkflowList(list(graph["steps"]), 256), list(state["nodes"]), boundedWorkflowList(list(state["actors"]), 128)
	statusBySite, statusByLane, statusByActor := map[string]string{}, map[workflowNodeKey]string{}, map[workflowNodeKey]string{}
	for _, raw := range nodes {
		n := object(raw)
		site, actor, status := str(n["siteId"]), str(n["actorSiteId"]), str(n["status"])
		runStatus := str(run["status"])
		if status == "running" && (runStatus == "completed" || runStatus == "failed" || runStatus == "cancelled" || runStatus == "interrupted" || runStatus == "stopped") {
			status = "interrupted"
		}
		statusBySite[site] = workflowAggregate(statusBySite[site], status)
		key := workflowNodeKey{Site: site, Actor: actor}
		statusByLane[key] = workflowAggregate(statusByLane[key], status)
		key.Ordinal = number(n["actorOrdinal"])
		statusByActor[key] = workflowAggregate(statusByActor[key], status)
	}
	lanes := map[string]string{}
	for _, raw := range list(graph["lanes"]) {
		lane := object(raw)
		lanes[str(lane["id"])] = str(lane["name"])
	}
	actors := map[string][]workflowActor{}
	for _, raw := range actorRows {
		actor := object(raw)
		id := str(actor["siteId"])
		name := str(actor["name"])
		if name == "" {
			name = lanes[id]
		}
		if name == "" {
			name = id
		}
		actors[id] = append(actors[id], workflowActor{Name: name, SessionID: str(actor["sessionId"]), SiteID: id, Ordinal: number(actor["ordinal"])})
	}
	phases := boundedWorkflowList(list(graph["phases"]), 64)
	stages := []workflowStage{}
	if len(phases) > 0 {
		for _, raw := range phases {
			p := object(raw)
			id, name := str(p["id"]), str(p["name"])
			if name == "" {
				name = "未分阶段"
			}
			stages = append(stages, workflowStage{ID: id, Name: name})
		}
	} else {
		for _, raw := range boundedWorkflowList(steps, 64) {
			step := object(raw)
			id, name := str(step["id"]), str(step["label"])
			if name == "" {
				name = id
			}
			stages = append(stages, workflowStage{ID: id, Name: name})
		}
	}
	for i := range stages {
		stage := &stages[i]
		byActor := map[string]bool{}
		var states []string
		for _, raw := range steps {
			step := object(raw)
			if (len(phases) > 0 && str(step["phase"]) != stage.ID) || (len(phases) == 0 && str(step["id"]) != stage.ID) {
				continue
			}
			site := str(step["source"])
			if site == "" {
				site = str(step["id"])
			}
			lane := str(step["lane"])
			status := "pending"
			if lane == "" {
				status = workflowAggregate(status, statusBySite[site])
			} else {
				status = workflowAggregate(status, statusByLane[workflowNodeKey{Site: site}])
				status = workflowAggregate(status, statusByLane[workflowNodeKey{Site: site, Actor: lane}])
			}
			states = append(states, status)
			if lane != "" && lane != "unknown" && lane != "workspace" && !byActor[lane] {
				members := actors[lane]
				if len(members) == 0 {
					name := lanes[lane]
					if name == "" {
						name = lane
					}
					members = []workflowActor{{Name: name, SiteID: lane}}
				}
				stage.Actors = append(stage.Actors, members...)
				byActor[lane] = true
			}
		}
		pending, complete := false, false
		for _, state := range states {
			pending = pending || state == "pending"
			complete = complete || state == "completed"
			stage.Status = workflowAggregate(stage.Status, state)
		}
		if pending && complete && stage.Status == "completed" && str(run["status"]) == "running" {
			stage.Status = "running"
		}
		if stage.Status == "running" && str(run["status"]) != "running" && str(run["status"]) != "" {
			stage.Status = "interrupted"
		}
		if stage.Status == "" {
			stage.Status = "pending"
		}
		for j := range stage.Actors {
			actor := &stage.Actors[j]
			actor.Status = "pending"
			for _, raw := range steps {
				step := object(raw)
				if str(step["lane"]) != actor.SiteID || (len(phases) > 0 && str(step["phase"]) != stage.ID) || (len(phases) == 0 && str(step["id"]) != stage.ID) {
					continue
				}
				site := str(step["source"])
				if site == "" {
					site = str(step["id"])
				}
				actor.Status = workflowAggregate(actor.Status, statusByActor[workflowNodeKey{Site: site, Actor: actor.SiteID, Ordinal: actor.Ordinal}])
			}
			if actor.Status == "running" && str(run["status"]) != "running" && str(run["status"]) != "" {
				actor.Status = "interrupted"
			}
		}
	}
	if len(stages) == 0 && len(actorRows) > 0 {
		stage := workflowStage{ID: "actors", Name: "协作角色", Status: str(run["status"])}
		for _, raw := range actorRows {
			actor := object(raw)
			if members := actors[str(actor["siteId"])]; len(members) > 0 {
				for _, member := range members {
					if member.Ordinal == number(actor["ordinal"]) {
						stage.Actors = append(stage.Actors, member)
						break
					}
				}
			}
		}
		stages = append(stages, stage)
	}
	edgeKey := "edges"
	if len(phases) > 0 {
		edgeKey = "phaseEdges"
	}
	names := map[string]string{}
	for _, stage := range stages {
		names[stage.ID] = stage.Name
	}
	var relations []workflowRelation
	for _, raw := range list(graph[edgeKey]) {
		e := object(raw)
		from, to := names[str(e["from"])], names[str(e["to"])]
		if from != "" && to != "" {
			relations = append(relations, workflowRelation{From: from, To: to, FromID: str(e["from"]), ToID: str(e["to"]), Certainty: str(e["certainty"])})
		}
	}
	return stages, relations
}

func workflowAggregate(a, b string) string {
	priority := map[string]int{"": 0, "pending": 1, "completed": 2, "running": 3, "interrupted": 4, "failed": 5}
	if priority[b] > priority[a] {
		return b
	}
	return a
}
func workflowDisplayStatus(status, runStatus string) string {
	if status == "pending" && runStatus == "completed" {
		return "未执行"
	}
	return workflowStatus(status)
}
func workflowColor(c *ui.Context, status string) ui.Color {
	switch status {
	case "running", "completed":
		return c.Theme().Accent
	case "failed", "errored":
		return c.Theme().Danger
	default:
		return c.Theme().TextMuted
	}
}

// Draw only engine-reported edges. Curves above the phase row represent
// forward, backward and self-loop dependencies without implying ordering
// between adjacent phases that have no edge.
func workflowGraph(c *ui.Context, stages []workflowStage, edges []workflowRelation) {
	if len(edges) == 0 {
		return
	}
	const pitch float32 = 162
	shown := min(len(edges), 6)
	height := float32(15 + shown*9)
	index := map[string]int{}
	for i, stage := range stages {
		index[stage.ID] = i
	}
	color := c.Theme().Accent.Alpha(.6)
	ui.Box(c).Width(float32(len(stages)-1)*pitch + 154).Height(height).Label("工作流依赖关系图").Draw(func(p *ui.Painter, r ui.Rect) {
		for i, edge := range edges[:shown] {
			from, ok1 := index[edge.FromID]
			to, ok2 := index[edge.ToID]
			if !ok1 || !ok2 {
				continue
			}
			x1, x2 := r.X+5+float32(from)*pitch, r.X+5+float32(to)*pitch
			base, top := r.Y+height-3, r.Y+float32(i*9+3)
			var path ui.Path
			if from == to {
				path.MoveTo(x1, base).LineTo(x1, top).LineTo(x1+20, top).LineTo(x1+20, base)
			} else {
				path.MoveTo(x1, base).LineTo(x1, top).LineTo(x2, top).LineTo(x2, base)
			}
			if edge.Certainty == "maybe" {
				path.Dash(0, 3, 3)
			}
			p.StrokePath(&path, 1.2, color)
			tip := x2
			if from == to {
				tip = x1 + 20
			}
			var arrow ui.Path
			arrow.MoveTo(tip-3, base-4).LineTo(tip, base).LineTo(tip+3, base-4)
			p.StrokePath(&arrow, 1.2, color)
		}
	})
}

func (a *preview) conversationWorkflow(c *ui.Context, s *session, m *message) {
	id := conversationWorkflowID(m)
	run := object(a.Live.Data["workflow-card/"+id])
	verified := id != "" && str(run["id"]) == id && str(run["parent_session_id"]) == s.RemoteID
	if id != "" && !verified {
		run = nil
	}
	name := str(run["name"])
	if name == "" {
		name = str(object(m.Args)["name"])
	}
	if name == "" {
		name = "动态工作流"
	}
	status := str(run["status"])
	if status == "" {
		if m.Status == "失败" {
			status = "failed"
		} else {
			status = "pending"
		}
	}
	stages, relations := projectWorkflow(run)
	ui.Column(c).Label("工作流卡片："+name).FillWidth().MinWidth(0).Padding(14, 16).Gap(12).Radius(10).Background(c.Theme().Surface).Border(1, c.Theme().Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			icon(c, "activity")
			ui.Text(c, name).FontSize(13).FontWeight(600).MinWidth(0).Grow(1).SingleLine()
			statusLabel := workflowDisplayStatus(status, status)
			if !a.Live.Connected {
				statusLabel = "未连接 · 最后状态 " + statusLabel
			}
			ui.Text(c, "● "+statusLabel).FontSize(11).TextColor(workflowColor(c, status)).Role(ui.RoleStatus)
		})
		if len(stages) == 0 {
			text := "正在准备运行，等待工作流状态…"
			if m.Status == "完成" && id == "" {
				text = "该运行没有可关联的记录，可在动态工作流设置中查看历史。"
			}
			if m.Status == "失败" {
				text = "工作流未能启动，请展开工作过程查看原因。"
			}
			if err := a.Live.ResourceErrors["workflow-card/"+id]; id != "" && err != "" {
				text = "暂时无法读取工作流状态：" + err
			}
			ui.Text(c, text).FontSize(12).TextColor(c.Theme().TextMuted).MaxLines(2)
		} else {
			ui.ScrollHorizontal(c).FillWidth().Children(func() {
				ui.Column(c).Gap(3).Children(func() {
					workflowGraph(c, stages, relations)
					ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
						for _, stage := range stages {
							ui.Column(c.Key(stage.ID)).Width(154).Gap(6).Children(func() {
								ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
									ui.Text(c, "●").FontSize(9).TextColor(workflowColor(c, stage.Status))
									ui.Text(c, stage.Name).FontSize(12).FontWeight(600).MinWidth(0).SingleLine().Tooltip(stage.Name)
								})
								ui.Text(c, workflowDisplayStatus(stage.Status, status)).FontSize(11).TextColor(c.Theme().TextMuted)
								for _, actor := range stage.Actors {
									actor := actor
									label := actor.Name
									if label == "" {
										label = "角色"
									}
									display := label
									if actor.Status != "" {
										display += " · " + workflowDisplayStatus(actor.Status, status)
									}
									button := flatButton(c.Key(actor.SessionID+label), "user", display).Label("打开角色会话：" + label).Justify(ui.Start).FontSize(11).MaxWidth(150).Disabled(actor.SessionID == "" || !internalLinkID.MatchString(actor.SessionID))
									button.OnClick(func() { a.openSessionSearchHit(sessionSearchHit{SessionID: actor.SessionID}) })
								}
							})
						}
					})
				})
			})
		}
		if len(relations) > 0 {
			parts := make([]string, 0, len(relations))
			for _, edge := range relations[:min(len(relations), 6)] {
				part := edge.From + " → " + edge.To
				if edge.Certainty == "maybe" {
					part += "（可能）"
				}
				parts = append(parts, part)
			}
			if len(relations) > 6 {
				parts = append(parts, fmt.Sprintf("另有 %d 条", len(relations)-6))
			}
			ui.Text(c, "关系 · "+strings.Join(parts, "  ·  ")).FontSize(11).TextColor(c.Theme().TextMuted).MaxLines(3)
		}
		graph, state := object(run["graph"]), object(run["state"])
		if len(list(graph["phases"])) > 64 || len(list(graph["steps"])) > 256 || (len(list(graph["phases"])) == 0 && len(list(graph["steps"])) > 64) || len(list(state["actors"])) > 128 {
			ui.Text(c, "工作流较大，此处仅展示部分阶段和角色；运行详情提供节点记录。").FontSize(11).TextColor(c.Theme().TextMuted)
		}
		if err := str(run["error"]); err != "" {
			ui.Text(c, err).FontSize(12).TextColor(c.Theme().Danger).MaxLines(3)
		}
		if result := run["result"]; result != nil {
			var text string
			if v, ok := result.(string); ok {
				text = v
			} else {
				data, _ := json.Marshal(result)
				text = string(data)
			}
			if len([]rune(text)) > 240 {
				text = string([]rune(text)[:240]) + "…"
			}
			if text != "" && text != "null" {
				ui.Text(c, "结果 · "+text).FontSize(12).MaxLines(4)
			}
		}
		if verified {
			ui.Row(c).Gap(8).Children(func() {
				flatButton(c, "external-link", "运行详情").FontSize(11).OnClick(func() {
					a.Live.WorkflowSelected = id
					a.openSettings("workflows")
					a.resource("workflow-detail", "/dynamic-workflows/"+url.PathEscape(id))
				})
				if status == "running" {
					flatButton(c, "stop", "取消运行").FontSize(11).OnClick(func() {
						a.requestConfirmation("取消工作流？", "已完成的操作不会撤销。", func() { a.workflowAction(id, "cancel", nil); delete(a.Live.Data, "workflow-card/"+id) })
					})
				}
			})
		}
	})
}
