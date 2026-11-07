package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/internal/agents/business"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/operations"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/nightgopher/easyagent/sdk/skill"
)

// TeamSessionSetup resolves a saved team into a leader session with the
// delegation tool. The team structure is frozen into the returned snapshot:
// later team edits do not affect sessions started before them, while member
// agent definitions are still re-resolved live at each delegation.
func (a *App) TeamSessionSetup(ctx context.Context, teamID string, cfg config.Config) (config.Config, runtime.Dependencies, business.TeamSnapshot, error) {
	team, err := a.Agents().GetTeam(ctx, teamID)
	if err != nil {
		return cfg, runtime.Dependencies{}, business.TeamSnapshot{}, err
	}
	if team.Disabled {
		return cfg, runtime.Dependencies{}, business.TeamSnapshot{}, fmt.Errorf("team %q is disabled", team.Name)
	}
	snapshot, err := a.teamSnapshotFromTeam(ctx, team)
	if err != nil {
		return cfg, runtime.Dependencies{}, business.TeamSnapshot{}, err
	}
	cfg, deps, err := a.teamDeps(ctx, snapshot, cfg)
	return cfg, deps, snapshot, err
}

// TeamSessionSetupFromSnapshot rebuilds a team session from its frozen
// snapshot (session reload after reconnect or restart).
func (a *App) TeamSessionSetupFromSnapshot(ctx context.Context, snapshot business.TeamSnapshot, cfg config.Config) (config.Config, runtime.Dependencies, error) {
	if snapshot.ID == "" || snapshot.LeaderID == "" {
		return cfg, runtime.Dependencies{}, fmt.Errorf("invalid team snapshot")
	}
	cfg, deps, err := a.teamDeps(ctx, snapshot, cfg)
	return cfg, deps, err
}

// teamSnapshotFromTeam freezes the team configuration, resolving members live:
// missing or disabled members stay in the snapshot as unavailable instead of
// blocking the whole team.
func (a *App) teamSnapshotFromTeam(ctx context.Context, team agentdata.Team) (business.TeamSnapshot, error) {
	snapshot := business.TeamSnapshot{
		ID:          team.ID,
		Name:        team.Name,
		Description: team.Description,
		LeaderID:    team.LeaderID,
		Workspace:   team.Workspace,
	}
	store := a.Agents()
	if _, err := store.GetAgent(ctx, team.LeaderID); err != nil {
		return business.TeamSnapshot{}, fmt.Errorf("team leader is unavailable: %w", err)
	}
	for _, m := range team.Members {
		def, err := store.GetAgent(ctx, m.AgentID)
		if err != nil {
			if errors.Is(err, agentdata.ErrNotFound) {
				snapshot.Unavailable = append(snapshot.Unavailable, business.UnavailableMember{AgentID: m.AgentID, Reason: "missing"})
				continue
			}
			return business.TeamSnapshot{}, err
		}
		if def.Disabled {
			snapshot.Unavailable = append(snapshot.Unavailable, business.UnavailableMember{AgentID: m.AgentID, Name: def.Name, Reason: "disabled"})
			continue
		}
		snapshot.Members = append(snapshot.Members, business.TeamMemberSnapshot{AgentID: def.ID, Name: def.Name, Role: m.Role})
	}
	if len(snapshot.Members) == 0 {
		return business.TeamSnapshot{}, fmt.Errorf("team %q has no available members", team.Name)
	}
	return snapshot, nil
}

// teamDeps wires the leader's session: workspace and model resolution mirror
// plain business sessions; the application wraps the leader with team context.
func (a *App) teamDeps(ctx context.Context, snapshot business.TeamSnapshot, cfg config.Config) (config.Config, runtime.Dependencies, error) {
	store := a.Agents()
	leader, err := store.GetAgent(ctx, snapshot.LeaderID)
	if err != nil {
		return cfg, runtime.Dependencies{}, err
	}
	if leader.Disabled {
		return cfg, runtime.Dependencies{}, agentdata.ErrDisabled
	}
	workspace, err := filepath.Abs(store.Workspace(leader))
	if err != nil {
		return cfg, runtime.Dependencies{}, err
	}
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return cfg, runtime.Dependencies{}, err
	}
	cfg.Workspace, cfg.AllowOutsideWorkspace, cfg.ExecutionMode = workspace, false, "local"
	if leader.Model != "" {
		if cfg.Provider == "anthropic" {
			cfg.AnthropicModel = leader.Model
		} else {
			cfg.OpenAIModel = leader.Model
		}
	}
	assembler := &business.MemberAssembler{
		Store:      store,
		Config:     cfg,
		Registry:   a.registry,
		Skills:     a.loadTeamSkills(cfg),
		Sessions:   a.sessionMgr,
		WorkReader: a.readWork,
	}
	teamApp := func(current agentdata.Agent) business.TeamApplication {
		return business.TeamApplication{
			Application: business.Application{Definition: current, Config: a.Config(), Store: store, Sessions: a.sessionMgr, WorkReader: a.readWork},
			Team:        snapshot,
			Members:     assembler,
		}
	}
	deps := a.SessionDepsWithApp("business")
	deps.BuildOperations = func(config.Config, string) *operations.Operations { return operations.NewLocalOperations() }
	deps.RefreshApplication = func(ctx context.Context) (runtime.Application, error) {
		current, err := store.GetAgent(ctx, leader.ID)
		if err != nil {
			return nil, err
		}
		if current.Disabled {
			return nil, agentdata.ErrDisabled
		}
		currentWorkspace, err := filepath.Abs(store.Workspace(current))
		if err != nil || currentWorkspace != workspace {
			return nil, fmt.Errorf("agent workspace changed; start a new session")
		}
		return teamApp(current), nil
	}
	deps.Application = teamApp(leader)
	return cfg, deps, nil
}

// loadTeamSkills loads the host skill catalog team members select from,
// applying the same host-level disabled-skill preferences as regular sessions.
func (a *App) loadTeamSkills(cfg config.Config) []skill.Skill {
	disabled, err := config.DisabledSkillPaths(cfg.DataDir)
	if err != nil {
		disabled = map[string]bool{}
	}
	var skills []skill.Skill
	result := skill.LoadFromDirs(a.GlobalSkillDirs()...)
	for _, loaded := range result.Skills {
		if !disabled[filepath.Clean(loaded.FilePath)] {
			skills = append(skills, loaded)
		}
	}
	return skills
}
