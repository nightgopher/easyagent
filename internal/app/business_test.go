package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/stretchr/testify/require"
)

func TestBusinessSessionIdentityPermissionsAndReload(t *testing.T) {
	ctx := context.Background()
	a := taskTestApp(t, []string{"write"})
	first, err := a.Agents().SaveAgent(ctx, agentdata.Agent{Name: "Support", Instructions: "Only assist customers", Model: "agent-model"})
	require.NoError(t, err)
	other, err := a.Agents().SaveAgent(ctx, agentdata.Agent{Name: "Sales", Instructions: "Only assist sales"})
	require.NoError(t, err)
	cfg, deps, err := a.SessionSetup(ctx, "", first.ID, a.Config())
	require.NoError(t, err)
	require.NotEqual(t, a.Config().Workspace, cfg.Workspace)
	require.NotEqual(t, a.Agents().Workspace(other), cfg.Workspace)
	sess, err := a.SessionStore().Create(ctx, runtime.AgentSessionOptions{Config: cfg}, deps)
	require.NoError(t, err)
	require.NoError(t, a.SessionManager().SaveAgentMeta(sess.SessionID(), sess.Workspace(), first.ID))
	require.Contains(t, sess.ToolNames(), "read")
	for _, name := range []string{"write", "bash", "task", "save_memory", "skill_market", "edit"} {
		require.NotContains(t, sess.ToolNames(), name)
	}
	require.Empty(t, sess.LoadedSkills())
	_, model := sess.ModelInfo()
	require.Equal(t, "agent-model", model)
	first.Tools = []string{"local_time"}
	first, err = a.Agents().SaveAgent(ctx, first)
	require.NoError(t, err)
	require.NoError(t, sess.RefreshTools(ctx))
	require.NotContains(t, sess.ToolNames(), "read")
	require.NoError(t, a.SessionStore().Delete(sess.SessionID()))
	restored, err := a.LoadSession(ctx, sess.SessionID())
	require.NoError(t, err)
	require.Equal(t, sess.Workspace(), restored.Workspace())
	require.NotContains(t, restored.ToolNames(), "read")
	infos, err := a.SessionManager().List(ctx)
	require.NoError(t, err)
	require.Equal(t, first.ID, infos[0].AgentID)
	first.Disabled = true
	_, err = a.Agents().SaveAgent(ctx, first)
	require.NoError(t, err)
	require.ErrorIs(t, restored.RefreshTools(ctx), agentdata.ErrDisabled)
	_, _, err = a.SessionSetup(ctx, "", "missing", a.Config())
	require.ErrorIs(t, err, agentdata.ErrNotFound)
	_, _, err = a.SessionSetup(ctx, "coding", first.ID, a.Config())
	require.Error(t, err)
	_, _, err = a.SessionSetup(ctx, "business", "", a.Config())
	require.Error(t, err)
}

func TestBusinessSessionDoesNotReadGlobalInstructions(t *testing.T) {
	a := taskTestApp(t, nil)
	ctx := context.Background()
	definition, err := a.Agents().SaveAgent(ctx, agentdata.Agent{Name: "Role", Instructions: "Own instructions"})
	require.NoError(t, err)
	cfg, deps, err := a.SessionSetup(ctx, "", definition.ID, a.Config())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Workspace, "AGENTS.md"), []byte("Do not inherit this project's coding identity"), 0600))
	application, err := deps.RefreshApplication(ctx)
	require.NoError(t, err)
	prompt := application.BuildPrompt(runtime.PromptBuildOptions{CWD: cfg.Workspace}, "", "")
	require.Contains(t, prompt, "Own instructions")
	require.NotContains(t, prompt, "coding identity")
}
