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

func TestBusinessToolSettingsUseSessionSnapshot(t *testing.T) {
	ctx := context.Background()
	a := taskTestApp(t, nil)
	definition, err := a.Agents().SaveAgent(ctx, agentdata.Agent{Name: "Research", Instructions: "Research", Tools: []string{"read", "web_search"}})
	require.NoError(t, err)
	newSession := func() *runtime.AgentSession {
		cfg, deps, err := a.SessionSetup(ctx, "", definition.ID, a.Config())
		require.NoError(t, err)
		sess, err := a.SessionStore().Create(ctx, runtime.AgentSessionOptions{Config: cfg}, deps)
		require.NoError(t, err)
		return sess
	}
	old := newSession()
	require.Contains(t, old.ToolNames(), "web_search")
	require.Contains(t, old.ToolNames(), "read")
	require.NoError(t, a.SetBuiltinToolSetting("web_search", false))
	require.NoError(t, a.SetBuiltinToolSetting("file_read", false))
	require.NoError(t, old.RefreshTools(ctx))
	require.Contains(t, old.ToolNames(), "web_search")
	require.Contains(t, old.ToolNames(), "read")
	current := newSession()
	require.NotContains(t, current.ToolNames(), "web_search")
	require.NotContains(t, current.ToolNames(), "read")
}

func TestStewardUsesOnlySelectedHostToolsAndOwnMemory(t *testing.T) {
	ctx := context.Background()
	a := taskTestApp(t, nil)
	definition, err := a.Agents().EnsureSteward(ctx)
	require.NoError(t, err)
	cfg, deps, err := a.SessionSetup(ctx, "", definition.ID, a.Config())
	require.NoError(t, err)
	sess, err := a.SessionStore().Create(ctx, runtime.AgentSessionOptions{Config: cfg}, deps)
	require.NoError(t, err)
	for _, name := range []string{"host_work_search", "host_session_search", "host_agents_list", "memory_search", "memory_save"} {
		require.Contains(t, sess.ToolNames(), name)
	}
	for _, name := range []string{"task", "bash", "read", "write", "web_fetch", "kb_search"} {
		require.NotContains(t, sess.ToolNames(), name)
	}
	require.Equal(t, a.Agents().Workspace(definition), cfg.Workspace)
	other, err := a.Agents().SaveAgent(ctx, agentdata.Agent{Name: "专属", Instructions: "自己的职责", MemoryEnabled: true})
	require.NoError(t, err)
	_, err = a.Agents().SaveMemory(ctx, other.ID, "", "user", "", agentdata.Memory{Key: "偏好", Content: "不要泄露"})
	require.NoError(t, err)
	memories, err := a.Agents().SearchMemories(ctx, definition.ID, cfg.Workspace, "")
	require.NoError(t, err)
	require.Empty(t, memories)
	_, otherDeps, err := a.SessionSetup(ctx, "", other.ID, a.Config())
	require.NoError(t, err)
	for _, tool := range otherDeps.Application.BuildTools(runtime.ToolBuildOptions{Workspace: t.TempDir()}) {
		require.NotContains(t, []string{"host_work_search", "host_session_search", "host_agents_list"}, tool.Name())
	}
}
