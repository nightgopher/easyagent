package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/internal/agents/business"
	"github.com/nightgopher/easyagent/internal/app"
	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

// teamFakeProvider routes by model in-process: the leader model first emits a
// team_delegate tool call, the member model answers, then the leader wraps up.
type teamFakeProvider struct {
	mu          sync.Mutex
	calls       int
	memberReq   *ai.StreamRequest
	leaderModel string
}

func (p *teamFakeProvider) Name() string { return "openai" }

func (p *teamFakeProvider) StreamSimple(ctx context.Context, req ai.SimpleStreamRequest) (*ai.EventStream, error) {
	return p.Stream(ctx, ai.StreamRequest{Model: req.Model, Messages: req.Messages, System: req.System, Tools: req.Tools})
}

func (p *teamFakeProvider) Stream(ctx context.Context, req ai.StreamRequest) (*ai.EventStream, error) {
	p.mu.Lock()
	p.calls++
	turn := p.calls
	member := req.Model.ID != p.leaderModel
	if member {
		copy := req
		p.memberReq = &copy
	}
	lastIsTool := len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role() == ai.RoleTool
	p.mu.Unlock()

	stream := ai.NewEventStream(8)
	go func() {
		defer stream.Close()
		usage := ai.Usage{InputTokens: turn * 100, OutputTokens: turn * 10}
		var partial ai.StreamAssistantMessage
		if member {
			partial = ai.StreamAssistantMessage{Usage: usage, Text: "member findings: alpha notes", StopReason: ai.StopReasonStop}
		} else if lastIsTool {
			partial = ai.StreamAssistantMessage{Usage: usage, Text: "leader summary done", StopReason: ai.StopReasonStop}
		} else {
			args, _ := json.Marshal(map[string]string{"member": "Alpha", "description": "research notes", "prompt": "Research the notes and report back."})
			partial = ai.StreamAssistantMessage{
				Usage: usage,
				ToolCalls: []ai.ToolCall{
					{ID: "call-1", Name: "team_delegate", Args: string(args)},
				},
				StopReason: ai.StopReasonToolUse,
			}
		}
		_ = stream.Push(ctx, ai.EventStart{Partial: partial})
		if partial.Text != "" {
			_ = stream.Push(ctx, ai.EventTextStart{ContentIndex: 0, Partial: partial})
			_ = stream.Push(ctx, ai.EventTextDelta{ContentIndex: 0, Delta: partial.Text, Partial: partial})
			_ = stream.Push(ctx, ai.EventTextEnd{ContentIndex: 0, Text: partial.Text, Partial: partial})
		}
		for i, tc := range partial.ToolCalls {
			_ = stream.Push(ctx, ai.EventToolCallStart{ContentIndex: i, Partial: partial})
			_ = stream.Push(ctx, ai.EventToolCallDelta{ContentIndex: i, Delta: tc.Args, Partial: partial})
			_ = stream.Push(ctx, ai.EventToolCallEnd{ContentIndex: i, ToolCall: tc, Partial: partial})
		}
		_ = stream.Push(ctx, ai.EventDone{Reason: partial.StopReason, Message: partial})
		stream.SetResult(partial, nil)
	}()
	return stream, nil
}

func newTeamTestServer(t *testing.T) (*Server, *teamFakeProvider) {
	t.Helper()
	fake := &teamFakeProvider{leaderModel: "lead-model"}
	cfg := config.Default()
	cfg.DataDir, cfg.Workspace = t.TempDir(), t.TempDir()
	cfg.AutoApprove = true
	cfg.MCPConfigPath = fmt.Sprintf("%s/mcp.json", cfg.DataDir)
	cfg.Provider, cfg.OpenAIAPIKey = "openai", "test-key"
	application, err := app.New(app.AppOptions{Config: cfg})
	require.NoError(t, err)
	application.RegisterProvider(fake)
	srv := New(application, nil)
	t.Cleanup(func() {
		srv.cancel()
		srv.runs.mu.Lock()
		var done []<-chan struct{}
		for _, run := range srv.runs.requests {
			done = append(done, run.done)
		}
		srv.runs.mu.Unlock()
		for _, channel := range done {
			<-channel
		}
		require.NoError(t, application.Close())
	})
	return srv, fake
}

func TestBusinessTeamsCRUD(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	w := businessRequest(t, s, "POST", "/agents", map[string]any{"name": "Lead", "instructions": "lead role"}, 201)
	var leader agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &leader))
	w = businessRequest(t, s, "POST", "/agents", map[string]any{"name": "Alpha", "instructions": "alpha role"}, 201)
	var alpha agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &alpha))

	body := map[string]any{"name": "Squad", "leader_id": leader.ID, "members": []map[string]any{{"agent_id": alpha.ID, "role": "research"}}}
	w = businessRequest(t, s, "POST", "/teams", body, 201)
	var team agentdata.Team
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &team))
	require.Equal(t, "team-", team.ID[:5])
	require.EqualValues(t, 1, team.Version)

	businessRequest(t, s, "GET", "/teams", nil, 200)
	businessRequest(t, s, "GET", "/teams/"+team.ID, nil, 200)
	businessRequest(t, s, "GET", "/teams/missing", nil, 404)

	team.Description = "research squad"
	w = businessRequest(t, s, "PUT", "/teams/"+team.ID, team, 200)
	var updated agentdata.Team
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	require.EqualValues(t, 2, updated.Version)
	businessRequest(t, s, "PUT", "/teams/"+team.ID, team, 409)

	businessRequest(t, s, "POST", "/teams", map[string]any{"name": "Bad", "leader_id": leader.ID, "members": []map[string]any{{"agent_id": "agent-missing"}}}, 400)
	businessRequest(t, s, "POST", "/teams", map[string]any{"name": "Bad", "leader_id": leader.ID}, 400)
	businessRequest(t, s, "POST", "/teams", map[string]any{"name": "Bad", "leader_id": leader.ID, "members": []map[string]any{{"agent_id": alpha.ID}}, "unexpected": true}, 400)
}

func TestTeamSessionStartsAndDelegates(t *testing.T) {
	s, fake := newTeamTestServer(t)
	// Distinct models let the fake provider route leader and member requests.
	w := businessRequest(t, s, "POST", "/agents", map[string]any{"name": "Lead", "instructions": "lead role", "model": "lead-model"}, 201)
	var leader agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &leader))
	w = businessRequest(t, s, "POST", "/agents", map[string]any{"name": "Alpha", "instructions": "alpha role", "model": "member-model-x"}, 201)
	var alpha agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &alpha))
	w = businessRequest(t, s, "POST", "/teams", map[string]any{"name": "Squad", "leader_id": leader.ID, "members": []map[string]any{{"agent_id": alpha.ID, "role": "research"}}}, 201)
	var team agentdata.Team
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &team))

	w = businessRequest(t, s, "POST", "/sessions", map[string]any{"team_id": team.ID}, 200)
	var session SessionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &session))

	// Session list exposes the team identity for clients.
	w = businessRequest(t, s, "GET", "/sessions", nil, 200)
	require.Contains(t, w.Body.String(), `"team_id":"`+team.ID+`"`)
	require.Contains(t, w.Body.String(), `"team_name":"Squad"`)

	run, _, err := s.startRun(session.ID, "research the notes", "req-team-e2e")
	require.NoError(t, err)
	_, err = s.waitRun(context.Background(), run)
	require.NoError(t, err)
	require.Equal(t, "completed", run.State)

	fake.mu.Lock()
	calls, memberReq := fake.calls, fake.memberReq
	fake.mu.Unlock()
	require.Equal(t, 3, calls, "leader + member + leader")
	require.NotNil(t, memberReq, "member was never called")

	// The member ran with its own identity: fresh context, its own system
	// prompt, and a read-only tool set without delegation or write access.
	require.Len(t, memberReq.Messages, 1, "member gets the task prompt only")
	require.Contains(t, memberReq.System, "alpha role")
	require.NotContains(t, memberReq.System, "lead role")
	require.Contains(t, memberReq.System, "Team member mode")
	toolNames := map[string]bool{}
	for _, spec := range memberReq.Tools {
		toolNames[spec.Name] = true
	}
	require.True(t, toolNames["read"])
	require.False(t, toolNames["write"], "member tools stay read-only")
	require.False(t, toolNames["team_delegate"], "members cannot delegate")

	// Usage accounting: leader turns plus the member's subagent usage.
	require.Equal(t, 200, run.SubagentUsage.InputTokens)
	require.Equal(t, 600, run.Usage.InputTokens, "leader 100+300, member 200")

	// The frozen snapshot survives a reload and stays independent of later
	// team edits.
	var snapshot business.TeamSnapshot
	require.NoError(t, s.app.SessionManager().TeamSessionMeta(session.ID, &snapshot))
	require.Equal(t, team.ID, snapshot.ID)
	require.Len(t, snapshot.Members, 1)
	cfg, deps, err := s.app.TeamSessionSetupFromSnapshot(context.Background(), snapshot, s.app.Config())
	require.NoError(t, err)
	teamApp, ok := deps.Application.(business.TeamApplication)
	require.True(t, ok, "reload should restore a team application")
	require.Equal(t, snapshot.ID, teamApp.Team.ID)
	require.NotEmpty(t, cfg.Workspace)

	// Editing the team afterwards does not rewrite the session's snapshot.
	extra := mustExtraAgent(t, s)
	team.Members = append(team.Members, agentdata.TeamMember{AgentID: extra})
	businessRequest(t, s, "PUT", "/teams/"+team.ID, team, 200)
	var reloaded business.TeamSnapshot
	require.NoError(t, s.app.SessionManager().TeamSessionMeta(session.ID, &reloaded))
	require.Len(t, reloaded.Members, 1, "session snapshot stays frozen")
}

func mustExtraAgent(t *testing.T, s *Server) string {
	t.Helper()
	w := businessRequest(t, s, "POST", "/agents", map[string]any{"name": "Gamma", "instructions": "gamma role"}, 201)
	var agent agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &agent))
	return agent.ID
}

func TestTeamSessionStartRejectsInvalidTeams(t *testing.T) {
	s, _ := newTeamTestServer(t)
	w := businessRequest(t, s, "POST", "/agents", map[string]any{"name": "Lead", "instructions": "lead role"}, 201)
	var leader agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &leader))
	w = businessRequest(t, s, "POST", "/agents", map[string]any{"name": "Alpha", "instructions": "alpha role"}, 201)
	var alpha agentdata.Agent
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &alpha))
	w = businessRequest(t, s, "POST", "/teams", map[string]any{"name": "Squad", "leader_id": leader.ID, "members": []map[string]any{{"agent_id": alpha.ID}}}, 201)
	var team agentdata.Team
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &team))

	businessRequest(t, s, "POST", "/sessions", map[string]any{"team_id": "team-missing"}, 404)
	businessRequest(t, s, "POST", "/sessions", map[string]any{"team_id": team.ID, "agent_id": leader.ID}, 400)
	businessRequest(t, s, "POST", "/sessions", map[string]any{"team_id": team.ID, "application": "coding"}, 400)
	businessRequest(t, s, "POST", "/sessions", map[string]any{"team_id": team.ID, "cwd": t.TempDir()}, 400)

	// A disabled leader blocks the session.
	leader.Disabled = true
	w = businessRequest(t, s, "PUT", "/agents/"+leader.ID, leader, 200)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &leader))
	businessRequest(t, s, "POST", "/sessions", map[string]any{"team_id": team.ID}, 409)
	leader.Disabled = false
	w = businessRequest(t, s, "PUT", "/agents/"+leader.ID, leader, 200)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &leader))

	// All members disabled: no session.
	alpha.Disabled = true
	w = businessRequest(t, s, "PUT", "/agents/"+alpha.ID, alpha, 200)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &alpha))
	businessRequest(t, s, "POST", "/sessions", map[string]any{"team_id": team.ID}, 400)

	// A disabled team is refused too.
	alpha.Disabled = false
	w = businessRequest(t, s, "PUT", "/agents/"+alpha.ID, alpha, 200)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &alpha))
	team.Disabled = true
	w = businessRequest(t, s, "PUT", "/teams/"+team.ID, team, 200)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &team))
	businessRequest(t, s, "POST", "/sessions", map[string]any{"team_id": team.ID}, 400)
}
