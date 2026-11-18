// Package stewardtools provides the steward's dispatch capability: spawning
// independent task sessions on the host. Unlike hosttools (read-only), these
// tools change host state, but only by creating fresh sessions — they never
// write into other agents' memory, knowledge or transcripts.
package stewardtools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/nightgopher/easyagent/sdk/agent"
)

// ToolName is the steward-only session dispatch tool.
const ToolName = "host_session_create"

// DispatchRequest describes a task session the steward wants to spawn.
// Application defaults to "coding"; AgentID selects a business agent instead
// (never the steward itself). An optional Prompt is delivered as the first
// task message through the session's queue.
type DispatchRequest struct {
	Title       string
	Cwd         string
	Application string
	AgentID     string
	Model       string
	Prompt      string
}

// Dispatcher creates the task session (and delivers the optional first
// prompt) and returns its session id plus whether the prompt was queued.
// The server injects the implementation; the tool stays transport-agnostic.
type Dispatcher func(ctx context.Context, req DispatchRequest) (sessionID string, queued bool, err error)

type Tool struct{ dispatch Dispatcher }

// Tools returns the steward dispatch tools, or nil when no dispatcher is
// available (e.g. CLI/TUI runs without a server).
func Tools(dispatch Dispatcher) []agent.Tool {
	if dispatch == nil {
		return nil
	}
	return []agent.Tool{Tool{dispatch}}
}

func (t Tool) Name() string                           { return ToolName }
func (t Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

func (t Tool) Description() string {
	return "Create a new independent task session on this host and return its session_id and easyagent://session/{id} link. Use it to hand concrete work (coding by default, or a configured business agent) to a dedicated session that runs in parallel with this one. Confirm missing key details (project directory, model) with the user before calling; pass prompt to queue the first task message so the session starts working at once. Never target the steward itself; this tool cannot touch other sessions' content."
}

func (t Tool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":       map[string]any{"type": "string", "maxLength": 160, "description": "short task label shown as the session title"},
			"cwd":         map[string]any{"type": "string", "maxLength": 1024, "description": "working directory for the task session"},
			"application": map[string]any{"type": "string", "enum": []string{"coding", "business"}, "description": "coding (default) for programming tasks, business with agent_id for a configured business agent"},
			"agent_id":    map[string]any{"type": "string", "maxLength": 128, "description": "business agent id (required when application=business); never the steward"},
			"model":       map[string]any{"type": "string", "maxLength": 128, "description": "model override for the task session"},
			"prompt":      map[string]any{"type": "string", "maxLength": 65536, "description": "optional first task message, queued and run by the new session"},
		},
		"additionalProperties": false,
	}
}

type arguments struct {
	Title       string `json:"title"`
	Cwd         string `json:"cwd"`
	Application string `json:"application"`
	AgentID     string `json:"agent_id"`
	Model       string `json:"model"`
	Prompt      string `json:"prompt"`
}

func clean(s string, maxRunes int) (string, bool) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) || utf8.RuneCountInString(s) > maxRunes {
		return "", false
	}
	return s, true
}

func (t Tool) Validate(p json.RawMessage) (json.RawMessage, error) {
	var args arguments
	d := json.NewDecoder(strings.NewReader(string(p)))
	d.DisallowUnknownFields()
	if err := d.Decode(&args); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("expected one argument object")
	}
	if len(p) == 0 || strings.TrimSpace(string(p)) == "null" {
		return nil, fmt.Errorf("expected an argument object")
	}
	for _, field := range []struct {
		value string
		max   int
	}{{args.Title, 160}, {args.Cwd, 1024}, {args.Model, 128}, {args.AgentID, 128}} {
		if _, ok := clean(field.value, field.max); !ok {
			return nil, fmt.Errorf("invalid field bounds")
		}
	}
	if prompt, ok := clean(args.Prompt, 65536); !ok || (args.Prompt != "" && prompt == "") {
		return nil, fmt.Errorf("invalid prompt")
	}
	switch args.Application {
	case "", "coding", "business":
	default:
		return nil, fmt.Errorf("application must be coding or business")
	}
	if args.AgentID == "agent-steward" {
		return nil, fmt.Errorf("the steward cannot spawn another steward session")
	}
	if args.Application == "business" && args.AgentID == "" {
		return nil, fmt.Errorf("agent_id is required when application is business")
	}
	return p, nil
}

func (t Tool) Execute(ctx context.Context, p json.RawMessage, _ func(agent.PartialResult)) (agent.ToolResult, error) {
	if _, err := t.Validate(p); err != nil {
		return agent.ToolResult{}, err
	}
	var args arguments
	if err := json.Unmarshal(p, &args); err != nil {
		return agent.ToolResult{}, err
	}
	req := DispatchRequest{
		Title:       strings.TrimSpace(args.Title),
		Cwd:         strings.TrimSpace(args.Cwd),
		Application: strings.TrimSpace(args.Application),
		AgentID:     strings.TrimSpace(args.AgentID),
		Model:       strings.TrimSpace(args.Model),
		Prompt:      strings.TrimSpace(args.Prompt),
	}
	sessionID, queued, err := t.dispatch(ctx, req)
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if sessionID == "" {
		return agent.ToolResult{Content: "dispatcher returned no session id", IsError: true}, nil
	}
	link := fmt.Sprintf("easyagent://session/%s", sessionID)
	result := map[string]any{
		"session_id":   sessionID,
		"session_link": link,
		"queued":       queued,
	}
	content := fmt.Sprintf("任务会话已创建：%s（%s）。", sessionID, link)
	if req.Prompt != "" {
		if queued {
			content += "首条任务消息已投递，会话将自行开始。"
		} else {
			content += "首条任务消息未投递（队列被暂停或已满），用户打开会话后可见。"
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		data = []byte("{}")
	}
	return agent.ToolResult{Content: content, UserFacing: content, Details: json.RawMessage(data)}, nil
}
