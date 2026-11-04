package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/internal/agents/business"
	"github.com/nightgopher/easyagent/internal/hosttools"
	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/operations"
	"github.com/nightgopher/easyagent/sdk/runtime"
)

func (a *App) Agents() *agentdata.Store { return agentdata.New(a.Config().DataDir) }

// SetWorkReader binds the host service's live view without introducing a
// dependency from application code onto the HTTP server implementation.
func (a *App) SetWorkReader(reader hosttools.WorkReader) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	a.workReader = reader
}
func (a *App) readWork(ctx context.Context, query, view string, offset int) (any, error) {
	a.cfgMu.Lock()
	reader := a.workReader
	a.cfgMu.Unlock()
	if reader == nil {
		return nil, fmt.Errorf("live work status is unavailable on this host")
	}
	return reader(ctx, query, view, offset)
}

// SessionSetup resolves a saved identity without falling back to Coding on an
// invalid/missing business agent. Model changes affect new sessions; permissions
// and instructions are reloaded before each subsequent turn.
func (a *App) SessionSetup(ctx context.Context, application, agentID string, cfg config.Config) (config.Config, runtime.Dependencies, error) {
	deps := a.SessionDepsWithApp(application)
	if agentID == "" {
		if application == "business" {
			return cfg, deps, fmt.Errorf("business sessions require agent_id")
		}
		if _, ok := a.applications[application]; application != "" && !ok {
			return cfg, deps, fmt.Errorf("unknown application %q", application)
		}
		return cfg, deps, nil
	}
	if application != "" && application != "business" {
		return cfg, deps, fmt.Errorf("agent_id cannot be combined with another application")
	}
	store := a.Agents()
	definition, err := store.GetAgent(ctx, agentID)
	if err != nil {
		return cfg, deps, err
	}
	if definition.Disabled {
		return cfg, deps, agentdata.ErrDisabled
	}
	workspace, err := filepath.Abs(store.Workspace(definition))
	if err != nil {
		return cfg, deps, err
	}
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return cfg, deps, err
	}
	cfg.Workspace, cfg.AllowOutsideWorkspace, cfg.ExecutionMode = workspace, false, "local"
	if definition.Model != "" {
		if cfg.Provider == "anthropic" {
			cfg.AnthropicModel = definition.Model
		} else {
			cfg.OpenAIModel = definition.Model
		}
	}
	deps.BuildOperations = func(config.Config, string) *operations.Operations { return operations.NewLocalOperations() }
	deps.RefreshApplication = func(ctx context.Context) (runtime.Application, error) {
		current, err := store.GetAgent(ctx, agentID)
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
		return business.Application{Definition: current, Config: a.Config(), Store: store, Sessions: a.sessionMgr, WorkReader: a.readWork}, nil
	}
	deps.Application = business.Application{Definition: definition, Config: cfg, Store: store, Sessions: a.sessionMgr, WorkReader: a.readWork}
	deps.ToolDiscoveryPolicy = func() (agent.ToolSearchMode, uint64) {
		mode, revision := a.toolDiscoveryPolicy()
		current, err := store.GetAgent(context.Background(), agentID)
		if err != nil || !slices.Contains(current.Tools, agent.SearchToolsName) {
			mode = agent.ToolSearchOff
		}
		return mode, revision
	}
	return cfg, deps, nil
}
