package coding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/nightgopher/easyagent/sdk/skill"
)

func toolNamed(t *testing.T, tools []agent.Tool, name string) agent.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %s is not built", name)
	return nil
}

// guardConfig is a deployment profile that lets the session change content and
// a scratch directory, and run an allowlisted bash.
func guardConfig(dataDir string) config.Config {
	return config.Config{
		DataDir:    dataDir,
		EnableBash: true,
		FilePolicy: &config.FilePolicy{WriteAllow: []string{"src/content/**", ".agents/tmp/**"}},
		BashPolicy: &config.BashPolicy{NPX: []string{"astro"}},
	}
}

func TestGuardProfileToolsRefuseWhatTheProfileForbids(t *testing.T) {
	dir := t.TempDir()
	app := CodingApplication{Cfg: guardConfig(dir)}
	tools := app.BuildTools(runtime.ToolBuildOptions{Workspace: dir, SessionID: "s"})

	bash := toolNamed(t, tools, "bash")
	for _, command := range []string{"git status --short", "npm run build", "echo hi > .agents/tmp/log.txt"} {
		args, err := json.Marshal(map[string]string{"command": command})
		require.NoError(t, err)
		_, err = bash.Validate(args)
		assert.NoError(t, err, "should pass the policy: %s", command)
	}
	for _, command := range []string{"git push origin main", "echo x > astro.config.mjs", "sudo ls"} {
		args, err := json.Marshal(map[string]string{"command": command})
		require.NoError(t, err)
		_, err = bash.Validate(args)
		assert.ErrorContains(t, err, "bash 策略拒绝", command)
		// A caller that skips Validate still cannot run it.
		_, err = bash.Execute(t.Context(), args, nil)
		assert.ErrorContains(t, err, "bash 策略拒绝", command)
	}

	write := toolNamed(t, tools, "write")
	args, err := json.Marshal(map[string]string{"path": "astro.config.mjs", "content": "x"})
	require.NoError(t, err)
	_, err = write.Execute(t.Context(), args, nil)
	assert.ErrorContains(t, err, "outside the writable paths")
	_, statErr := os.Stat(filepath.Join(dir, "astro.config.mjs"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestToolsWithoutGuardProfileKeepTheirDefaultBehaviour(t *testing.T) {
	dir := t.TempDir()
	app := CodingApplication{Cfg: config.Config{DataDir: dir, EnableBash: true}}
	tools := app.BuildTools(runtime.ToolBuildOptions{Workspace: dir})
	bash := toolNamed(t, tools, "bash")
	args, err := json.Marshal(map[string]string{"command": "cd /tmp && sudo git push --force"})
	require.NoError(t, err)
	_, err = bash.Validate(args)
	require.NoError(t, err)
	assert.NotContains(t, bash.Description(), "operator policy")
}

func TestFilterSkillsHidesDeniedSkills(t *testing.T) {
	skills := []skill.Skill{{Name: "brand"}, {Name: "create-page"}, {Name: "impeccable"}}
	app := CodingApplication{Cfg: config.Config{Skills: &config.SkillsConfig{Deny: []string{"impeccable"}}}}
	kept := app.FilterSkills(skills)
	require.Len(t, kept, 2)
	assert.Equal(t, "brand", kept[0].Name)
	assert.Equal(t, "create-page", kept[1].Name)

	plain := CodingApplication{}
	assert.Len(t, plain.FilterSkills(skills), 3)
}

func TestBuildPromptInlinesMandatorySkills(t *testing.T) {
	dir := t.TempDir()
	skills := []skill.Skill{
		{Name: "brand", Description: "brand facts", Content: "Use the deep teal palette.", FilePath: filepath.Join(dir, "SKILL.md")},
		{Name: "create-page", Description: "new pages", Content: "Collect only the four inputs.", FilePath: filepath.Join(dir, "SKILL.md")},
	}
	app := CodingApplication{Cfg: config.Config{Skills: &config.SkillsConfig{AlwaysInline: []string{"brand", "missing"}}}}
	prompt := app.BuildPrompt(runtime.PromptBuildOptions{CWD: dir, Skills: skills}, "coding", "")
	assert.Contains(t, prompt, "## Mandatory skill: brand")
	assert.Contains(t, prompt, "Use the deep teal palette.")
	// An inlined skill is still listed with its file, so its references stay reachable.
	assert.Contains(t, prompt, "<name>brand</name>")
	assert.NotContains(t, prompt, "Collect only the four inputs.")
	assert.NotContains(t, prompt, "Mandatory skill: missing")

	plain := CodingApplication{}.BuildPrompt(runtime.PromptBuildOptions{CWD: dir}, "coding", "")
	assert.NotContains(t, plain, "Mandatory skill")
}
