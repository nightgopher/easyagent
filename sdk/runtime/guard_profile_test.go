package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
	"github.com/nightgopher/easyagent/sdk/skill"
	"github.com/stretchr/testify/require"
)

type guardTestApplication struct {
	confirmationTestApplication
	prompts []PromptBuildOptions
	filter  bool
}

func (a *guardTestApplication) BuildPrompt(opts PromptBuildOptions, _, _ string) string {
	a.prompts = append(a.prompts, opts)
	return "guard test"
}

func (a *guardTestApplication) FilterSkills(skills []skill.Skill) []skill.Skill {
	if a.filter {
		return nil
	}
	return skills
}

func guardTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir, cfg.Workspace, cfg.Provider = t.TempDir(), t.TempDir(), "openai"
	return cfg
}

func writeGuardTestSkill(t *testing.T, root, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

const validGuardSkill = "---\nname: required\ndescription: Required instructions\n---\nAlways check the project.\n"

func TestConfigSkillDirsUsedForCreateReopenAndRefresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := guardTestConfig(t)
	root, market, cliRoot := t.TempDir(), t.TempDir(), t.TempDir()
	cfg.Skills = &config.SkillsConfig{Dirs: []string{root}, Deny: []string{"denied"}, AlwaysInline: []string{"required"}}
	cfg.SkillMarketEnabled, cfg.SkillMarketSkillDir = true, market
	wanted := writeGuardTestSkill(t, root, "required", validGuardSkill)
	writeGuardTestSkill(t, root, "denied", "---\nname: denied\ndescription: Hidden\n---\nHidden body\n")
	for _, dir := range []string{filepath.Join(home, ".agents", "skills"), filepath.Join(home, ".claude", "skills"), filepath.Join(cfg.Workspace, ".agents", "skills"), market, cliRoot} {
		writeGuardTestSkill(t, dir, "injected", "---\nname: injected\ndescription: Untrusted\n---\nInjection\n")
	}
	app := &guardTestApplication{}
	deps := Dependencies{SessionMgr: sessionmgr.NewManager(cfg.DataDir), Registry: providers.NewRegistry(), Application: app}
	// Like POST /sessions: the entrypoint supplies Config only, not SkillDirs.
	s, err := NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg}, deps)
	require.NoError(t, err)
	assertCatalog := func(s *AgentSession) {
		t.Helper()
		loaded := s.LoadedSkills()
		require.Len(t, loaded, 1)
		require.Equal(t, wanted, loaded[0].FilePath)
		require.Len(t, app.prompts[len(app.prompts)-1].Skills, 1)
	}
	assertCatalog(s)
	id := s.sessionID
	require.NoError(t, s.Close())
	// A restore path must apply the active profile, not default/personal roots.
	s, err = NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg, SessionID: id, SkillDirs: []string{cliRoot}}, deps)
	require.NoError(t, err)
	defer s.Close()
	assertCatalog(s)
	require.NoError(t, s.RefreshTools(context.Background()))
	assertCatalog(s)
}

func TestExplicitSkillRootsExcludeMarketWithoutGuardConfig(t *testing.T) {
	cfg := guardTestConfig(t)
	root, market := t.TempDir(), t.TempDir()
	cfg.SkillMarketSkillDir = market
	writeGuardTestSkill(t, root, "required", validGuardSkill)
	writeGuardTestSkill(t, market, "injected", "---\nname: injected\ndescription: Market\n---\nInjection\n")
	s, err := NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg, SkillDirs: []string{root}}, Dependencies{
		SessionMgr: sessionmgr.NewManager(cfg.DataDir), Registry: providers.NewRegistry(), Application: &guardTestApplication{},
	})
	require.NoError(t, err)
	defer s.Close()
	require.Len(t, s.LoadedSkills(), 1)
	require.Equal(t, "required", s.LoadedSkills()[0].Name)
}

func TestDefaultSkillDiscoveryStillIncludesProjectPersonalAndMarket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := guardTestConfig(t)
	cfg.SkillMarketSkillDir = t.TempDir()
	for name, dir := range map[string]string{
		"project":  filepath.Join(cfg.Workspace, ".agents", "skills"),
		"personal": filepath.Join(home, ".agents", "skills"),
		"claude":   filepath.Join(home, ".claude", "skills"),
		"market":   cfg.SkillMarketSkillDir,
	} {
		writeGuardTestSkill(t, dir, name, "---\nname: "+name+"\ndescription: Default skill\n---\nInstructions\n")
	}
	s, err := NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg}, Dependencies{
		SessionMgr: sessionmgr.NewManager(cfg.DataDir), Registry: providers.NewRegistry(), Application: &guardTestApplication{},
	})
	require.NoError(t, err)
	defer s.Close()
	names := []string{}
	for _, loaded := range s.LoadedSkills() {
		names = append(names, loaded.Name)
	}
	require.ElementsMatch(t, []string{"project", "personal", "claude", "market"}, names)
}

func TestRequiredSkillDisableFailsRefreshAndReopen(t *testing.T) {
	cfg := guardTestConfig(t)
	root := t.TempDir()
	cfg.Skills = &config.SkillsConfig{Dirs: []string{root}, AlwaysInline: []string{"required"}}
	path := writeGuardTestSkill(t, root, "required", validGuardSkill)
	app := &guardTestApplication{}
	deps := Dependencies{SessionMgr: sessionmgr.NewManager(cfg.DataDir), Registry: providers.NewRegistry(), Application: app}
	s, err := NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg}, deps)
	require.NoError(t, err)
	id := s.sessionID
	require.NoError(t, config.SetSkillEnabled(cfg.DataDir, path, false))
	require.ErrorContains(t, s.RefreshTools(context.Background()), "required skill")
	require.Len(t, app.prompts, 1, "a failed rebuild must not produce an incomplete prompt")
	require.NoError(t, s.Close())
	s, err = NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg, SessionID: id}, deps)
	require.ErrorContains(t, err, "required skill")
	require.Nil(t, s)
	require.Len(t, app.prompts, 1)
}

func TestRequiredSkillsFailClosedBeforeBuildingPrompt(t *testing.T) {
	for _, scenario := range []string{"missing", "denied", "disabled", "model disabled", "filtered", "invalid yaml", "unterminated", "empty body", "invalid description", "invalid type", "duplicate metadata"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := guardTestConfig(t)
			root := t.TempDir()
			cfg.Skills = &config.SkillsConfig{Dirs: []string{root}, AlwaysInline: []string{"required"}}
			body := validGuardSkill
			switch scenario {
			case "invalid yaml":
				body = "---\nname: required\ndescription: Required\nbroken: [\n---\nInstructions\n"
			case "unterminated":
				body = "---\nname: required\ndescription: Required\nInstructions\n"
			case "empty body":
				body = "---\nname: required\ndescription: Required\n---\n"
			case "invalid description":
				body = "---\nname: required\n---\nInstructions\n"
			case "invalid type":
				body = "---\nname: required\ndescription: Required\ndisable-model-invocation: bogus\n---\nInstructions\n"
			case "duplicate metadata":
				body = "---\nname: required\nname: required\ndescription: Required\n---\nInstructions\n"
			case "model disabled":
				body = "---\nname: required\ndescription: Required\ndisable-model-invocation: true\n---\nInstructions\n"
			case "denied":
				cfg.Skills.Deny = []string{"required"}
			}
			if scenario != "missing" {
				path := writeGuardTestSkill(t, root, "required", body)
				if scenario == "disabled" {
					require.NoError(t, config.SetSkillEnabled(cfg.DataDir, path, false))
				}
			}
			app := &guardTestApplication{filter: scenario == "filtered"}
			deps := Dependencies{SessionMgr: sessionmgr.NewManager(cfg.DataDir), Registry: providers.NewRegistry(), Application: app}
			s, err := NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg}, deps)
			require.ErrorContains(t, err, "required skill")
			require.Nil(t, s)
			require.Empty(t, app.prompts)
			require.Zero(t, app.buildToolsCalls)
		})
	}
}

func TestProjectContextRuntimeScopeIsOptInOnCreateAndReopen(t *testing.T) {
	root := t.TempDir()
	workspace, home := filepath.Join(root, "project"), filepath.Join(root, "home")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	require.NoError(t, os.MkdirAll(home, 0o700))
	t.Setenv("EA_HOME", home)
	for path, body := range map[string]string{filepath.Join(root, "AGENTS.md"): "ancestor", filepath.Join(workspace, "AGENTS.md"): "project", filepath.Join(home, "AGENTS.md"): "personal"} {
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	for _, scope := range []string{"", "all", "project"} {
		t.Run(scope, func(t *testing.T) {
			cfg := guardTestConfig(t)
			cfg.Workspace, cfg.ContextScope, cfg.AllowOutsideWorkspace, cfg.AutoApprove = workspace, scope, true, true
			cfg.Skills = &config.SkillsConfig{Dirs: []string{t.TempDir()}}
			app := &guardTestApplication{}
			deps := Dependencies{SessionMgr: sessionmgr.NewManager(cfg.DataDir), Registry: providers.NewRegistry(), Application: app}
			id := ""
			for range 2 {
				s, err := NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg, SessionID: id}, deps)
				require.NoError(t, err)
				id = s.sessionID
				contents := []string{}
				for _, file := range app.prompts[len(app.prompts)-1].ContextFiles {
					contents = append(contents, file.Content)
				}
				if scope == "project" {
					require.Equal(t, []string{"project"}, contents)
					require.False(t, s.cfg.AllowOutsideWorkspace)
				} else {
					require.Contains(t, contents, "ancestor")
					require.Contains(t, contents, "personal")
				}
				require.NoError(t, s.Close())
			}
		})
	}
}

func TestProjectContextCanonicalizesWorkspace(t *testing.T) {
	cfg := guardTestConfig(t)
	canonical, err := filepath.EvalSymlinks(cfg.Workspace)
	require.NoError(t, err)
	alias := filepath.Join(t.TempDir(), "workspace-link")
	require.NoError(t, os.Symlink(canonical, alias))
	cfg.Workspace, cfg.ContextScope = alias, "project"
	cfg.Skills = &config.SkillsConfig{Dirs: []string{t.TempDir()}}
	s, err := NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg}, Dependencies{
		SessionMgr: sessionmgr.NewManager(cfg.DataDir), Registry: providers.NewRegistry(), Application: &guardTestApplication{},
	})
	require.NoError(t, err)
	defer s.Close()
	require.Equal(t, canonical, s.Workspace())
}

func TestProjectContextRequiresPinnedLocalWorkspace(t *testing.T) {
	for _, scenario := range []string{"empty", "relative", "missing", "file", "ssh", "invalid scope"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := guardTestConfig(t)
			cfg.ContextScope = "project"
			switch scenario {
			case "empty":
				cfg.Workspace = ""
			case "relative":
				cfg.Workspace = "project"
			case "missing":
				cfg.Workspace = filepath.Join(t.TempDir(), "missing")
			case "file":
				cfg.Workspace = filepath.Join(t.TempDir(), "file")
				require.NoError(t, os.WriteFile(cfg.Workspace, nil, 0o600))
			case "ssh":
				cfg.ExecutionMode = "ssh"
			case "invalid scope":
				cfg.ContextScope = "projet"
			}
			s, err := NewAgentSession(context.Background(), AgentSessionOptions{Config: cfg}, Dependencies{})
			require.Error(t, err)
			require.Nil(t, s)
		})
	}
}
