package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nightgopher/easyagent/internal/dynamicflow"
	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func workflowTestApp(t *testing.T, cfg config.Config) *App {
	t.Helper()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	t.Cleanup(gateway.Close)
	cfg.Provider = "openai"
	cfg.OpenAIAPIKey = "test"
	cfg.OpenAIBaseURL = gateway.URL
	cfg.OpenAIModel = "model-a"
	if cfg.DataDir == "" {
		cfg.DataDir = t.TempDir()
	}
	app, err := New(AppOptions{Config: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Close() })
	return app
}
func TestWorkflowActorRetainsWorkspaceModelAndCannotRecurse(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	app := workflowTestApp(t, cfg)
	parentCfg := app.cfg
	parentCfg.Workspace = t.TempDir()
	parentCfg.OpenAIModel = "model-b"
	parent, err := app.SessionStore().Create(context.Background(), runtime.AgentSessionOptions{Config: parentCfg}, app.SessionDeps())
	require.NoError(t, err)
	require.Contains(t, parent.ToolNames(), "create_workflow")
	parent.SetConfirmFunc(func(context.Context, agent.ConfirmationRequest) agent.ConfirmDecision {
		return agent.ConfirmDecision{Approved: false, Reason: "denied"}
	})
	host := workflowHost{app}
	policy, err := host.Prepare(context.Background(), parent.SessionID())
	require.NoError(t, err)
	id, err := host.Actor(context.Background(), parent.SessionID(), policy, dynamicflow.Persona{Name: "worker", System: "review"}, "")
	require.NoError(t, err)
	child, ok := app.SessionStore().Get(id)
	require.True(t, ok)
	require.NotContains(t, child.ToolNames(), "create_workflow")
	require.NotContains(t, child.ToolNames(), "resume_workflow")
	require.Equal(t, parentCfg.Workspace, child.Workspace())
	provider, model := child.ModelInfo()
	require.Equal(t, "openai", provider)
	require.Equal(t, "model-b", model)
	require.False(t, child.ConfirmationCallback()(context.Background(), agent.ConfirmationRequest{}).Approved)
	require.NoError(t, app.SessionStore().Delete(id)) // closes runtime; session file remains
	restored, err := app.loadWorkflowActor(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, parentCfg.Workspace, restored.Workspace())
	_, model = restored.ModelInfo()
	require.Equal(t, "model-b", model)
	_, err = host.Actor(context.Background(), "", policy, dynamicflow.Persona{Name: "worker"}, id)
	require.ErrorContains(t, err, "需要原会话")
}
func TestWorkflowToolsRespectBlockListAndExplainSlashInSystemPrompt(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockedTools = []string{"create_workflow"}
	app := workflowTestApp(t, cfg)
	sess, err := app.NewSession(context.Background())
	require.NoError(t, err)
	require.NotContains(t, sess.ToolNames(), "create_workflow")
	require.NotContains(t, app.ToolNames(), "create_workflow")
	wrapper := workflowApplication{app: app, Application: app.application}
	require.Contains(t, wrapper.BuildPrompt(runtime.PromptBuildOptions{}, "", ""), "/workflow followed by whitespace")
}

func TestWorkflowNewActorAfterRestartUsesCapturedPolicy(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		t.Run(fmt.Sprint(confirm), func(t *testing.T) {
			cfg := config.Default()
			cfg.DataDir = t.TempDir()
			original := workflowTestApp(t, cfg)
			parentCfg := original.cfg
			parentCfg.Workspace = t.TempDir()
			parentCfg.OpenAIModel = "model-b"
			parent, err := original.SessionStore().Create(context.Background(), runtime.AgentSessionOptions{Config: parentCfg}, original.SessionDeps())
			require.NoError(t, err)
			if confirm {
				parent.SetConfirmFunc(func(context.Context, agent.ConfirmationRequest) agent.ConfirmDecision {
					return agent.ConfirmDecision{Approved: true}
				})
			}
			host := workflowHost{original}
			policy, err := host.Prepare(context.Background(), parent.SessionID())
			require.NoError(t, err)
			// A first actor exists; the later actor has never been created when service stops.
			_, err = host.Actor(context.Background(), parent.SessionID(), policy, dynamicflow.Persona{Name: "first"}, "")
			require.NoError(t, err)
			require.NoError(t, original.Close())
			restored := workflowTestApp(t, cfg)
			_, loaded := restored.SessionStore().Get(parent.SessionID())
			require.False(t, loaded)
			id, err := (workflowHost{restored}).Actor(context.Background(), parent.SessionID(), policy, dynamicflow.Persona{Name: "later"}, "")
			if confirm {
				require.ErrorContains(t, err, "需要原会话")
				return
			}
			require.NoError(t, err)
			actor, ok := restored.SessionStore().Get(id)
			require.True(t, ok)
			require.Equal(t, parentCfg.Workspace, actor.Workspace())
			_, model := actor.ModelInfo()
			require.Equal(t, "model-b", model)
			require.NotContains(t, actor.ToolNames(), "create_workflow")
		})
	}
}

func TestWorkflowCapturedToolPolicyCannotWidenAfterRestart(t *testing.T) {
	cfg := config.Default()
	cfg.AllowedTools = []string{"read"}
	cfg.EnableBash = true
	cfg.AllowOutsideWorkspace = true
	record := actorConfig{AllowedTools: []string{"read", "write"}}
	record.apply(&cfg)
	require.Equal(t, []string{"read"}, cfg.AllowedTools)
	require.False(t, cfg.EnableBash)
	require.False(t, cfg.AllowOutsideWorkspace)
	cfg.AllowedTools = []string{"bash"}
	record.apply(&cfg)
	require.Equal(t, []string{"__workflow_no_tools__"}, cfg.AllowedTools)
}

// Exercise the real Node adapter, SQLite journal and an actual child session using a local model fixture.
func TestWorkflowExecutesRealActorSession(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"actor result\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer gateway.Close()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Workspace = t.TempDir()
	cfg.Provider = "openai"
	cfg.OpenAIBaseURL = gateway.URL
	cfg.OpenAIAPIKey = "test"
	cfg.OpenAIModel = "model-a"
	application, err := New(AppOptions{Config: cfg})
	require.NoError(t, err)
	defer application.Close()
	if err := application.DynamicWorkflows().Available(); err != nil {
		t.Skipf("Node unavailable: %v", err)
	}
	parent, err := application.NewSession(context.Background())
	require.NoError(t, err)
	parent.SetConfirmFunc(func(context.Context, agent.ConfirmationRequest) agent.ConfirmDecision {
		return agent.ConfirmDecision{Approved: true}
	})
	tool := &workflowTool{app: application, name: "create_workflow", parent: parent.SessionID()}
	params, err := json.Marshal(map[string]string{"name": "test", "script": `const a=agent("researcher"); return await a.ask("investigate");`})
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), params, nil)
	require.NoError(t, err)
	require.False(t, result.IsError, result.Content)
	require.Contains(t, result.Content, "actor result")
	runs := application.DynamicWorkflows().List()
	require.Len(t, runs, 1)
	require.Equal(t, "completed", runs[0].Status)
	require.Len(t, runs[0].Actors, 1)
}

func TestWorkflowActorConfigRejectsUnsafeIDs(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	application := workflowTestApp(t, cfg)
	for _, id := range []string{"", ".", "..", "../outside", `..\outside`} {
		require.ErrorContains(t, application.saveActorConfig(id, actorConfig{}), "invalid workflow actor")
		_, err := application.readActorConfig(id)
		require.ErrorContains(t, err, "invalid workflow actor")
	}
}
