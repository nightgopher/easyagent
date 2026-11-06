// Team sessions: a saved business agent leads a fixed roster of member
// agents. The leader keeps its normal business application; this file adds the
// team roster to its prompt and a delegation tool that runs each member as a
// fresh child agent assembled from the member's own saved definition — its
// role instructions, model, tool whitelist, skills, knowledge bindings and
// memory — never from the leader's context.
package business

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/internal/hosttools"
	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/models"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/operations"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
	"github.com/nightgopher/easyagent/sdk/skill"
)

// TeamDelegateToolName is the tool a team leader uses to assign work.
const TeamDelegateToolName = "team_delegate"

// DefaultMemberMaxTurns bounds one member delegation when not overridden.
const DefaultMemberMaxTurns = 50

// TeamMemberSnapshot freezes one member's identity for the session lifetime.
type TeamMemberSnapshot struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
	Role    string `json:"role,omitempty"`
}

// UnavailableMember records why a configured member cannot be delegated to.
type UnavailableMember struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name,omitempty"`
	Reason  string `json:"reason"` // "missing" or "disabled"
}

// TeamSnapshot is the team configuration frozen when a session starts. Later
// team edits do not affect running sessions; member agent definitions are
// still resolved live at each delegation.
type TeamSnapshot struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	LeaderID    string               `json:"leader_id"`
	Members     []TeamMemberSnapshot `json:"members"`
	Unavailable []UnavailableMember  `json:"unavailable,omitempty"`
	// Workspace optionally binds member read access to one shared directory
	// (explicit team configuration; empty means each member uses its own).
	Workspace string `json:"workspace,omitempty"`
}

// memberSafeTools bounds team members to read-only tools. A member's own
// whitelist can only narrow this set further, never widen it.
var memberSafeTools = []string{
	"read", "local_time", "web_fetch", "web_search",
	"kb_search", "kb_read", "memory_search", "read_skill",
	"host_work_search", "host_session_search", "host_agents_list",
}

const memberSystemSuffix = `

# Team member mode
You are running as a team member for the team lead. Complete the assigned task on your own: you cannot ask the user questions and nobody will reply to you mid-task. You run read-only: report findings instead of making changes. Your final message is the only thing returned to the team lead, so make it a self-contained report: state what you actually did and found, cite exact file paths or sources where relevant, and say plainly what could not be done or remains uncertain. Never claim work you did not perform.`

// MemberAssembler builds child agents for team members from live definitions.
type MemberAssembler struct {
	Store      *agentdata.Store
	Config     config.Config
	Registry   *providers.Registry
	Skills     []skill.Skill
	Sessions   *sessionmgr.Manager
	WorkReader hosttools.WorkReader
}

// TeamApplication wraps the leader's business application with team context.
type TeamApplication struct {
	Application
	Team    TeamSnapshot
	Members *MemberAssembler
}

var _ runtime.Application = TeamApplication{}

func (a TeamApplication) BuildTools(o runtime.ToolBuildOptions) []agent.Tool {
	list := a.Application.BuildTools(o)
	// Starting a team session is the user's explicit authorization for the
	// leader to coordinate members; session-level blocks still win.
	if slices.Contains(o.BlockedTools, TeamDelegateToolName) {
		return list
	}
	return append(list, &teamDelegateTool{
		team:         a.Team,
		members:      a.Members,
		fileOps:      o.FileOps,
		maxOutputLen: o.MaxOutputLen,
	})
}

func (a TeamApplication) BuildPrompt(o runtime.PromptBuildOptions, profile, goal string) string {
	return a.Application.BuildPrompt(o, profile, goal) + a.teamPrompt()
}

func (a TeamApplication) teamPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n# Team: %s\n", a.Team.Name)
	if a.Team.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", a.Team.Description)
	}
	b.WriteString("You are the leader of this team. Members are saved business agents with their own roles, tools, knowledge and memory; delegate with the team_delegate tool when a member's expertise fits part of the task. Members run read-only, cannot see this conversation, and delegations issued in the same turn run in parallel, so give each one a self-contained prompt with every detail it needs. Report only work that actually completed: if a member failed, was never delegated to, or is unavailable, say so plainly instead of presenting it as done. Handle simple requests yourself.\n")
	if len(a.Team.Members) > 0 {
		b.WriteString("\nTeam members:\n")
		for _, m := range a.Team.Members {
			role := m.Role
			if role == "" {
				role = "see member instructions"
			}
			fmt.Fprintf(&b, "- %s (%s): %s\n", m.Name, m.AgentID, role)
		}
	}
	if len(a.Team.Unavailable) > 0 {
		b.WriteString("\nUnavailable members (delegating to them fails; do not assign them work):\n")
		for _, m := range a.Team.Unavailable {
			name := m.Name
			if name == "" {
				name = m.AgentID
			}
			reason := "member agent is disabled"
			if m.Reason == "missing" {
				reason = "member agent no longer exists"
			}
			fmt.Fprintf(&b, "- %s (%s)\n", name, reason)
		}
	}
	if a.Team.Workspace != "" {
		fmt.Fprintf(&b, "\nTeam workspace for members: %s. Members' file access is bound to this directory.\n", a.Team.Workspace)
	}
	return b.String()
}

// ────────────────────────────────────────────────────────────────────────────
//  team_delegate tool
// ────────────────────────────────────────────────────────────────────────────

type teamDelegateTool struct {
	team         TeamSnapshot
	members      *MemberAssembler
	fileOps      operations.FileOperations
	maxOutputLen int
}

type teamDelegateParams struct {
	Member      string `json:"member"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	MaxTurns    *int   `json:"max_turns,omitempty"`
}

func (t *teamDelegateTool) Name() string { return TeamDelegateToolName }

func (t *teamDelegateTool) Description() string {
	roster := make([]string, 0, len(t.team.Members))
	for _, m := range t.team.Members {
		entry := fmt.Sprintf("%s (id %s)", m.Name, m.AgentID)
		if m.Role != "" {
			entry += ": " + m.Role
		}
		roster = append(roster, entry)
	}
	return fmt.Sprintf("Delegate a self-contained task to one member of team %q. Each member is a saved business agent with its own role, tools, knowledge and memory; it runs read-only, cannot see this conversation, and its final report is the only thing returned. Available members: %s. Delegations issued in the same turn run in parallel.", t.team.Name, strings.Join(roster, "; "))
}

func (t *teamDelegateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"member":      map[string]any{"type": "string", "description": "Team member to delegate to: the member's agent id or its exact name."},
			"description": map[string]any{"type": "string", "description": "A short (3-6 word) label for the delegation, shown to the user."},
			"prompt":      map[string]any{"type": "string", "description": "Complete instructions for the member. It cannot see this conversation, so include the goal, relevant paths and exactly what to report back."},
			"max_turns":   map[string]any{"type": "integer", "minimum": 1, "description": "Maximum model turns for the member; defaults to the member limit (50 by default)."},
		},
		"required": []string{"member", "description", "prompt"},
	}
}

func (t *teamDelegateTool) Validate(raw json.RawMessage) (json.RawMessage, error) {
	var params teamDelegateParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	params.Member = strings.TrimSpace(params.Member)
	if params.Member == "" {
		return nil, fmt.Errorf("member is required")
	}
	if strings.TrimSpace(params.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if strings.TrimSpace(params.Description) == "" {
		params.Description = "team delegation"
	}
	if params.MaxTurns != nil && *params.MaxTurns < 1 {
		return nil, fmt.Errorf("max_turns must be positive")
	}
	return json.Marshal(params)
}

// IsConcurrencySafe lets several member delegations run in parallel; members
// are read-only and each runs in its own fresh agent.
func (t *teamDelegateTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (t *teamDelegateTool) Execute(ctx context.Context, raw json.RawMessage, onUpdate func(agent.PartialResult)) (agent.ToolResult, error) {
	validated, err := t.Validate(raw)
	if err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	var params teamDelegateParams
	if err := json.Unmarshal(validated, &params); err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	member, ok := t.resolveMember(params.Member)
	if !ok {
		return agent.ToolResult{IsError: true}, fmt.Errorf("unknown team member %q; available members: %s", params.Member, t.memberList())
	}
	def, err := t.members.Store.GetAgent(ctx, member.AgentID)
	if err != nil {
		return agent.ToolResult{IsError: true}, fmt.Errorf("team member %q no longer exists", member.Name)
	}
	if def.Disabled {
		return agent.ToolResult{IsError: true}, fmt.Errorf("team member %q is disabled", member.Name)
	}
	maxTurns := DefaultMemberMaxTurns
	if params.MaxTurns != nil {
		maxTurns = *params.MaxTurns
	}
	child, model, err := t.members.memberAgent(def, t.team.Workspace, t.fileOps, t.maxOutputLen, maxTurns)
	if err != nil {
		return agent.ToolResult{IsError: true}, err
	}

	var mu sync.Mutex
	steps, usage := 0, ai.Usage{}
	unsubscribe := child.Subscribe(func(_ context.Context, event agent.AgentEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch e := event.(type) {
		case agent.EventToolExecutionStart:
			steps++
			if onUpdate != nil {
				onUpdate(agent.PartialResult{Content: fmt.Sprintf("%s · step %d · %s", member.Name, steps, e.ToolName)})
			}
		case agent.EventUsage:
			usage.Add(e.Usage)
		}
	})
	defer unsubscribe()

	answer, err := child.Prompt(ctx, ai.NewTextUserMessage(params.Prompt))
	mu.Lock()
	accounting := &ai.SubagentUsage{Model: model.ID, Provider: model.Provider, Usage: usage}
	footer := fmt.Sprintf("\n\n[team member %q: %d tool calls, %d input / %d output tokens]", member.Name, steps, usage.InputTokens, usage.OutputTokens)
	mu.Unlock()
	if err != nil {
		content := fmt.Sprintf("team member %q failed: %v", member.Name, err)
		if text := strings.TrimSpace(answer.Text); text != "" {
			content += "\n\nPartial report:\n" + text
		}
		return agent.ToolResult{IsError: true, Content: content + footer, SubagentUsage: accounting}, err
	}
	text := strings.TrimSpace(answer.Text)
	if text == "" {
		text = "(the member finished without a final report)"
	}
	return agent.ToolResult{Content: text + footer, SubagentUsage: accounting}, nil
}

func (t *teamDelegateTool) resolveMember(ref string) (TeamMemberSnapshot, bool) {
	for _, m := range t.team.Members {
		if m.AgentID == ref || m.Name == ref {
			return m, true
		}
	}
	return TeamMemberSnapshot{}, false
}

func (t *teamDelegateTool) memberList() string {
	names := make([]string, 0, len(t.team.Members))
	for _, m := range t.team.Members {
		names = append(names, m.Name+" ("+m.AgentID+")")
	}
	return strings.Join(names, ", ")
}

// ────────────────────────────────────────────────────────────────────────────
//  Member assembly
// ────────────────────────────────────────────────────────────────────────────

// modelFor resolves the member's model: its own configured model, falling back
// to the session's provider default.
func (m *MemberAssembler) modelFor(def agentdata.Agent) (ai.Model, error) {
	id := def.Model
	if id == "" {
		if m.Config.Provider == "anthropic" {
			id = m.Config.AnthropicModel
		} else {
			id = m.Config.OpenAIModel
		}
	}
	if id == "" {
		return ai.Model{}, fmt.Errorf("no model configured for team member %q", def.Name)
	}
	model := ai.Model{ID: id, Name: id, Provider: m.Config.Provider, ContextWindow: models.ContextWindow(id), MaxTokens: models.MaxOutput(id)}
	if m.Config.Provider == "deepv" {
		// Same per-request cap the session runtime applies to EasyCode.
		if model.ContextWindow > 160000 {
			model.ContextWindow = 160000
		}
		if model.MaxTokens > 32000 {
			model.MaxTokens = 32000
		}
	}
	if m.Registry == nil {
		return ai.Model{}, fmt.Errorf("member provider registry is unavailable")
	}
	if _, ok := m.Registry.Get(model.Provider); !ok {
		return ai.Model{}, fmt.Errorf("member provider %q is not registered", model.Provider)
	}
	return model, nil
}

// assembledMember captures a member's independently assembled execution
// context: its own system prompt, tool set, model and file workspace.
type assembledMember struct {
	system    string
	tools     []agent.Tool
	model     ai.Model
	workspace string
}

// assembleMember builds the member's context from its own live definition:
// prompt and tools come from a business Application bound to the member, never
// from the leader's context. Tools are additionally intersected with the
// read-only member allowlist.
func (m *MemberAssembler) assembleMember(def agentdata.Agent, teamWorkspace string, fileOps operations.FileOperations, maxOutputLen int) (assembledMember, error) {
	workspace, err := m.memberWorkspace(def, teamWorkspace)
	if err != nil {
		return assembledMember{}, err
	}
	model, err := m.modelFor(def)
	if err != nil {
		return assembledMember{}, err
	}
	memberSkills := []skill.Skill{}
	for _, s := range m.Skills {
		if slices.Contains(def.Skills, s.Name) {
			memberSkills = append(memberSkills, s)
		}
	}
	if fileOps == nil {
		fileOps = operations.NewLocalOperations().Files
	}
	if maxOutputLen <= 0 {
		maxOutputLen = 51200
	}
	app := Application{Definition: def, Config: m.Config, Store: m.Store, Sessions: m.Sessions, WorkReader: m.WorkReader}
	tools := app.BuildTools(runtime.ToolBuildOptions{
		Workspace:    workspace,
		MaxOutputLen: maxOutputLen,
		FileOps:      fileOps,
		AllowedTools: memberSafeTools,
	})
	system := app.BuildPrompt(runtime.PromptBuildOptions{CWD: workspace, Skills: memberSkills}, "", "") + memberSystemSuffix
	return assembledMember{system: system, tools: tools, model: model, workspace: workspace}, nil
}

// memberAgent wraps the assembled context in a fresh child agent.
func (m *MemberAssembler) memberAgent(def agentdata.Agent, teamWorkspace string, fileOps operations.FileOperations, maxOutputLen, maxTurns int) (*agent.Agent, ai.Model, error) {
	parts, err := m.assembleMember(def, teamWorkspace, fileOps, maxOutputLen)
	if err != nil {
		return nil, ai.Model{}, err
	}
	child := agent.New(agent.Options{
		Model:              parts.model,
		Registry:           m.Registry,
		System:             parts.system,
		Tools:              parts.tools,
		MaxTurns:           maxTurns,
		LoopDetectSettings: agent.DefaultLoopDetectSettings(),
		// Member tools are read-only in this version; if a future tool asks
		// for confirmation, refuse instead of silently inheriting approval.
		ConfirmFunc: func(context.Context, agent.ConfirmationRequest) agent.ConfirmDecision {
			return agent.ConfirmDecision{Approved: false, Reason: "团队成员工具执行前的交互确认暂不支持，已拒绝该操作"}
		},
	})
	return child, parts.model, nil
}

// memberWorkspace resolves the directory the member's file access is bound
// to: the explicit team workspace when configured, otherwise the member's own
// workspace (created on demand, exactly like its standalone sessions).
func (m *MemberAssembler) memberWorkspace(def agentdata.Agent, teamWorkspace string) (string, error) {
	root := teamWorkspace
	if root == "" {
		root = m.Store.Workspace(def)
		if err := os.MkdirAll(root, 0700); err != nil {
			return "", err
		}
	}
	return filepath.Abs(root)
}
