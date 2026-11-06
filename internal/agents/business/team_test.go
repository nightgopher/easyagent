package business

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/stretchr/testify/require"
)

// scriptedProvider replays fixed responses in order and records requests.
type scriptedProvider struct {
	mu        sync.Mutex
	responses []scriptedResponse
	calls     []ai.StreamRequest
	index     int
	// block makes every call wait for ctx cancellation.
	block bool
}

type scriptedResponse struct {
	text      string
	toolCalls []ai.ToolCall
	stop      ai.StopReason
	usage     ai.Usage
}

func (p *scriptedProvider) Name() string { return "mock_test" }

func (p *scriptedProvider) StreamSimple(ctx context.Context, req ai.SimpleStreamRequest) (*ai.EventStream, error) {
	return p.Stream(ctx, ai.StreamRequest{Model: req.Model, Messages: req.Messages, System: req.System, Tools: req.Tools})
}

func (p *scriptedProvider) Stream(ctx context.Context, req ai.StreamRequest) (*ai.EventStream, error) {
	p.mu.Lock()
	p.calls = append(p.calls, req)
	index := p.index
	p.index++
	block := p.block
	p.mu.Unlock()

	stream := ai.NewEventStream(8)
	go func() {
		defer stream.Close()
		if block {
			<-ctx.Done()
			stream.SetResult(ai.StreamAssistantMessage{}, ctx.Err())
			return
		}
		resp := p.responses[index%len(p.responses)]
		partial := ai.StreamAssistantMessage{Usage: resp.usage, Text: resp.text, ToolCalls: resp.toolCalls, StopReason: resp.stop}
		_ = stream.Push(ctx, ai.EventStart{Partial: partial})
		if resp.text != "" {
			_ = stream.Push(ctx, ai.EventTextStart{ContentIndex: 0, Partial: partial})
			_ = stream.Push(ctx, ai.EventTextDelta{ContentIndex: 0, Delta: resp.text, Partial: partial})
			_ = stream.Push(ctx, ai.EventTextEnd{ContentIndex: 0, Text: resp.text, Partial: partial})
		}
		for i, tc := range resp.toolCalls {
			_ = stream.Push(ctx, ai.EventToolCallStart{ContentIndex: i, Partial: partial})
			_ = stream.Push(ctx, ai.EventToolCallDelta{ContentIndex: i, Delta: tc.Args, Partial: partial})
			_ = stream.Push(ctx, ai.EventToolCallEnd{ContentIndex: i, ToolCall: tc, Partial: partial})
		}
		_ = stream.Push(ctx, ai.EventDone{Reason: resp.stop, Message: partial})
		stream.SetResult(partial, nil)
	}()
	return stream, nil
}

func teamFixture(t *testing.T) (store *agentdata.Store, leader, alpha, beta agentdata.Agent) {
	t.Helper()
	store = agentdata.New(t.TempDir())
	ctx := context.Background()
	leader = agentdata.Agent{Name: "Lead", Instructions: "You are the lead.", Tools: agentdata.DefaultTools()}
	leader, err := store.SaveAgent(ctx, leader)
	require.NoError(t, err)
	alpha = agentdata.Agent{Name: "Alpha", Instructions: "Alpha research role.", Tools: agentdata.DefaultTools(), Workspace: t.TempDir()}
	alpha, err = store.SaveAgent(ctx, alpha)
	require.NoError(t, err)
	beta = agentdata.Agent{
		Name: "Beta", Instructions: "Beta review role.", Tools: append(agentdata.DefaultTools(), "memory_search"),
		Workspace: t.TempDir(), MemoryEnabled: true, Model: "member-model-x",
	}
	beta, err = store.SaveAgent(ctx, beta)
	require.NoError(t, err)
	return store, leader, alpha, beta
}

func testAssembler(store *agentdata.Store, provider *scriptedProvider) *MemberAssembler {
	registry := providers.NewRegistry()
	registry.Register(provider)
	return &MemberAssembler{
		Store:    store,
		Config:   config.Config{Provider: "mock_test", OpenAIModel: "lead-model"},
		Registry: registry,
	}
}

func testSnapshot(leader, alpha, beta agentdata.Agent) TeamSnapshot {
	return TeamSnapshot{
		ID: "team-1", Name: "Squad", Description: "research and review",
		LeaderID: leader.ID,
		Members: []TeamMemberSnapshot{
			{AgentID: alpha.ID, Name: alpha.Name, Role: "research"},
			{AgentID: beta.ID, Name: beta.Name},
		},
	}
}

func TestTeamApplicationPromptAndTools(t *testing.T) {
	store, leader, alpha, beta := teamFixture(t)
	app := TeamApplication{
		Application: Application{Definition: leader, Config: config.Config{}, Store: store},
		Team:        testSnapshot(leader, alpha, beta),
		Members:     testAssembler(store, &scriptedProvider{}),
	}
	prompt := app.BuildPrompt(runtime.PromptBuildOptions{CWD: "/tmp/lead"}, "", "")
	require.Contains(t, prompt, "You are Lead, a persistent business assistant")
	require.Contains(t, prompt, "# Team: Squad")
	require.Contains(t, prompt, "research and review")
	require.Contains(t, prompt, "Alpha ("+alpha.ID+"): research")
	require.Contains(t, prompt, "Beta ("+beta.ID+")")

	tools := app.BuildTools(runtime.ToolBuildOptions{Workspace: "/tmp/lead"})
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	require.True(t, names["team_delegate"], "want team_delegate, got %v", names)
	require.True(t, names["read"], "leader keeps its own read tool")

	// Session-level blocks still win over the delegation tool.
	blocked := app.BuildTools(runtime.ToolBuildOptions{Workspace: "/tmp/lead", BlockedTools: []string{TeamDelegateToolName}})
	for _, tool := range blocked {
		require.NotEqual(t, TeamDelegateToolName, tool.Name())
	}

	unavailable := testSnapshot(leader, alpha, beta)
	unavailable.Members = unavailable.Members[:1]
	unavailable.Unavailable = []UnavailableMember{{AgentID: beta.ID, Name: "Beta", Reason: "disabled"}}
	prompt = TeamApplication{Application: app.Application, Team: unavailable, Members: app.Members}.
		BuildPrompt(runtime.PromptBuildOptions{CWD: "/tmp/lead"}, "", "")
	require.Contains(t, prompt, "Unavailable members")
	require.Contains(t, prompt, "Beta (member agent is disabled)")
}

func TestTeamDelegateUnknownMember(t *testing.T) {
	store, leader, alpha, beta := teamFixture(t)
	tool := &teamDelegateTool{team: testSnapshot(leader, alpha, beta), members: testAssembler(store, &scriptedProvider{})}
	raw, err := tool.Validate([]byte(`{"member":"Nobody","description":"d","prompt":"p"}`))
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), raw, nil)
	require.Error(t, err)
	require.True(t, result.IsError)
	require.Contains(t, err.Error(), "unknown team member")
	require.Contains(t, err.Error(), alpha.Name)
}

func TestMemberAssemblyIsolation(t *testing.T) {
	store, _, alpha, beta := teamFixture(t)
	assembler := testAssembler(store, &scriptedProvider{})

	// Knowledge binding for beta only.
	source, err := store.SaveSource(context.Background(), agentdata.KnowledgeSource{Name: "docs", Path: t.TempDir()})
	require.NoError(t, err)
	beta.Knowledge = []agentdata.KnowledgeBinding{{SourceID: source.ID, Access: "read"}}
	beta, err = store.SaveAgent(context.Background(), beta)
	require.NoError(t, err)

	alphaParts, err := assembler.assembleMember(alpha, "", nil, 0)
	require.NoError(t, err)
	require.Equal(t, "lead-model", alphaParts.model.ID, "member without own model falls back to session model")

	// Alpha's system prompt: its own role, never the leader's, and no memory
	// guidance because Alpha has no memory.
	require.Contains(t, alphaParts.system, "Alpha research role.")
	require.NotContains(t, alphaParts.system, "You are the lead.")
	require.NotContains(t, alphaParts.system, "search current memory")
	alphaNames := map[string]bool{}
	for _, tool := range alphaParts.tools {
		alphaNames[tool.Name()] = true
	}
	require.True(t, alphaNames["read"])
	require.True(t, alphaNames["local_time"])
	require.False(t, alphaNames["write"], "write stays out of the read-only member set")
	require.False(t, alphaNames["ask_user_question"])
	require.False(t, alphaNames["memory_search"], "no memory tools for members without memory")
	require.False(t, alphaNames["kb_search"], "no knowledge tools without bindings")
	require.False(t, alphaNames["team_delegate"], "members cannot delegate further")

	betaParts, err := assembler.assembleMember(beta, "", nil, 0)
	require.NoError(t, err)
	require.Equal(t, "member-model-x", betaParts.model.ID, "member model wins over session default")
	require.Contains(t, betaParts.system, "Beta review role.")
	require.Contains(t, betaParts.system, "search current memory", "beta keeps its own memory guidance")
	betaNames := map[string]bool{}
	for _, tool := range betaParts.tools {
		betaNames[tool.Name()] = true
	}
	require.True(t, betaNames["memory_search"], "beta keeps its own memory search")
	require.False(t, betaNames["memory_save"], "member memory writes are not part of the read-only set")
	require.True(t, betaNames["kb_search"], "beta keeps its own knowledge binding")
	require.NotContains(t, betaParts.system, "You are the lead.")
}

func TestMemberWorkspaceBinding(t *testing.T) {
	store, _, alpha, _ := teamFixture(t)
	assembler := testAssembler(store, &scriptedProvider{})

	parts, err := assembler.assembleMember(alpha, "", nil, 0)
	require.NoError(t, err)
	require.Equal(t, alpha.Workspace, parts.workspace)
	var readRoot string
	for _, tool := range parts.tools {
		if tool.Name() == "read" {
			raw, verr := tool.Validate(json.RawMessage(`{"path":"probe.txt"}`))
			require.NoError(t, verr)
			result, xerr := tool.Execute(context.Background(), raw, nil)
			require.True(t, xerr != nil || result.IsError, "probe read should fail with an absolute path hint")
			if xerr != nil {
				readRoot = xerr.Error()
			} else {
				readRoot = result.Content
			}
		}
	}
	require.Contains(t, readRoot, alpha.Workspace, "member reads resolve inside its own workspace")

	// An explicit team workspace overrides the member's own directory.
	teamDir := t.TempDir()
	parts, err = assembler.assembleMember(alpha, teamDir, nil, 0)
	require.NoError(t, err)
	require.Equal(t, teamDir, parts.workspace)
	for _, tool := range parts.tools {
		if tool.Name() == "read" {
			require.NoError(t, os.WriteFile(filepath.Join(teamDir, "shared.txt"), []byte("team data"), 0600))
			raw, verr := tool.Validate(json.RawMessage(`{"path":"shared.txt"}`))
			require.NoError(t, verr)
			result, xerr := tool.Execute(context.Background(), raw, nil)
			require.NoError(t, xerr)
			require.False(t, result.IsError)
			require.Contains(t, result.Content, "team data")
		}
	}
	require.DirExists(t, alpha.Workspace, "member workspace created on demand")
}

func TestTeamDelegateExecutesMember(t *testing.T) {
	store, _, alpha, _ := teamFixture(t)
	require.NoError(t, os.MkdirAll(alpha.Workspace, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(alpha.Workspace, "notes.txt"), []byte("alpha findings"), 0600))

	provider := &scriptedProvider{responses: []scriptedResponse{
		{toolCalls: []ai.ToolCall{{ID: "call-1", Name: "read", Args: `{"path":"notes.txt"}`}}, stop: ai.StopReasonToolUse, usage: ai.Usage{InputTokens: 10, OutputTokens: 5}},
		{text: "Alpha read the notes: alpha findings", stop: ai.StopReasonStop, usage: ai.Usage{InputTokens: 7, OutputTokens: 3}},
	}}
	assembler := testAssembler(store, provider)
	tool := &teamDelegateTool{team: TeamSnapshot{ID: "team-1", Name: "Squad", Members: []TeamMemberSnapshot{{AgentID: alpha.ID, Name: alpha.Name}}}, members: assembler}

	var mu sync.Mutex
	var updates []string
	raw, err := tool.Validate([]byte(`{"member":"` + alpha.Name + `","description":"research notes","prompt":"Read notes.txt and report"}`))
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), raw, func(pr agent.PartialResult) {
		mu.Lock()
		updates = append(updates, pr.Content)
		mu.Unlock()
	})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Contains(t, result.Content, "alpha findings")
	require.Contains(t, result.Content, `[team member "Alpha": 1 tool calls, 17 input / 8 output tokens]`)
	require.NotNil(t, result.SubagentUsage)
	require.Equal(t, "lead-model", result.SubagentUsage.Model)
	require.Equal(t, 17, result.SubagentUsage.Usage.InputTokens)
	mu.Lock()
	require.Len(t, updates, 1)
	require.Contains(t, updates[0], "Alpha · step 1 · read")
	mu.Unlock()

	// The member conversation is a fresh context: first request carries only
	// the delegation prompt, and the member system prompt is its own role.
	require.GreaterOrEqual(t, len(provider.calls), 2)
	require.Len(t, provider.calls[0].Messages, 1)
	require.Contains(t, string(provider.calls[0].System), "Alpha research role.")
	require.Contains(t, string(provider.calls[0].System), "Team member mode")
}

func TestTeamDelegateDisabledAndMissingMembers(t *testing.T) {
	store, _, alpha, beta := teamFixture(t)
	snapshot := TeamSnapshot{ID: "team-1", Name: "Squad", Members: []TeamMemberSnapshot{
		{AgentID: alpha.ID, Name: alpha.Name}, {AgentID: beta.ID, Name: beta.Name},
	}}
	tool := &teamDelegateTool{team: snapshot, members: testAssembler(store, &scriptedProvider{})}

	disabled := alpha
	disabled.Disabled = true
	_, err := store.SaveAgent(context.Background(), disabled)
	require.NoError(t, err)
	raw, verr := tool.Validate([]byte(`{"member":"` + alpha.ID + `","description":"d","prompt":"p"}`))
	require.NoError(t, verr)
	result, xerr := tool.Execute(context.Background(), raw, nil)
	require.Error(t, xerr)
	require.True(t, result.IsError)
	require.Contains(t, xerr.Error(), "is disabled")

	ghost := TeamSnapshot{ID: "team-1", Name: "Squad", Members: []TeamMemberSnapshot{{AgentID: "agent-ghost", Name: "Ghost"}}}
	ghostTool := &teamDelegateTool{team: ghost, members: tool.members}
	raw, verr = ghostTool.Validate([]byte(`{"member":"agent-ghost","description":"d","prompt":"p"}`))
	require.NoError(t, verr)
	result, xerr = ghostTool.Execute(context.Background(), raw, nil)
	require.Error(t, xerr)
	require.True(t, result.IsError)
	require.Contains(t, xerr.Error(), "no longer exists")
}

func TestTeamDelegateCancelsWithContext(t *testing.T) {
	store, _, alpha, _ := teamFixture(t)
	provider := &scriptedProvider{
		responses: []scriptedResponse{{text: "late answer", stop: ai.StopReasonStop}},
		block:     true,
	}
	tool := &teamDelegateTool{
		team:    TeamSnapshot{ID: "team-1", Name: "Squad", Members: []TeamMemberSnapshot{{AgentID: alpha.ID, Name: alpha.Name}}},
		members: testAssembler(store, provider),
	}
	raw, err := tool.Validate([]byte(`{"member":"` + alpha.ID + `","description":"d","prompt":"p"}`))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	result, err := tool.Execute(ctx, raw, nil)
	require.Error(t, err)
	require.True(t, result.IsError)
	require.Contains(t, result.Content, "failed")
}

func TestTeamDelegateToolIsConcurrencySafe(t *testing.T) {
	tool := &teamDelegateTool{}
	var checker any = tool
	if c, ok := checker.(agent.ConcurrencySafeChecker); ok {
		require.True(t, c.IsConcurrencySafe(json.RawMessage(`{}`)))
	} else {
		t.Fatal("team_delegate should implement ConcurrencySafeChecker")
	}
}
