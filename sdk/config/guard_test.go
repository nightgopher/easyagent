package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestGuardProfileAbsentKeepsDefaults(t *testing.T) {
	cfg := Default()
	require.NoError(t, cfg.LoadFromYAML(writeConfig(t, t.TempDir(), "enable_bash: true\n")))
	assert.True(t, cfg.EnableBash)
	assert.Nil(t, cfg.Skills)
	assert.Nil(t, cfg.FilePolicy)
	assert.Nil(t, cfg.BashPolicy)
	assert.Nil(t, cfg.Submit)
	assert.Empty(t, cfg.PromptTemplate)
}

const guardProfileYAML = `strict_config: true
prompt_file: SYSTEM_PROMPT.md
skills:
  dirs: [skills-extra]
  deny: [impeccable]
  always_inline: [brand]
file_policy:
  write_allow: ["src/content/**", "public/**"]
  write_deny: ["src/data/secrets/**"]
bash_policy:
  allow: [jq]
  deny: [find]
  deny_args: ["gitlab.example.com/api"]
  npx: [astro]
submit:
  gate: ["npm run lint", "npm run build"]
  include: ["src/content/**"]
  branch_prefixes: ["bot/", "demo/"]
  base_branch: main
  remote: origin
  gate_timeout_seconds: 120
  preview_url: http://test.example.com/
`

func TestGuardProfileLoadsAllSections(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SYSTEM_PROMPT.md"), []byte("You are the website operator.\n"), 0o600))

	cfg := Default()
	require.NoError(t, cfg.LoadFromYAML(writeConfig(t, dir, guardProfileYAML)))

	assert.Equal(t, "You are the website operator.\n", cfg.PromptTemplate)
	assert.Equal(t, "SYSTEM_PROMPT.md", cfg.PromptFile)
	require.NotNil(t, cfg.Skills)
	assert.Equal(t, []string{filepath.Join(dir, "skills-extra")}, cfg.Skills.Dirs)
	assert.Equal(t, []string{"impeccable"}, cfg.Skills.Deny)
	assert.Equal(t, []string{"brand"}, cfg.Skills.AlwaysInline)
	require.NotNil(t, cfg.FilePolicy)
	assert.Equal(t, []string{"src/content/**", "public/**"}, cfg.FilePolicy.WriteAllow)
	assert.Equal(t, []string{"src/data/secrets/**"}, cfg.FilePolicy.WriteDeny)
	require.NotNil(t, cfg.BashPolicy)
	assert.Equal(t, []string{"jq"}, cfg.BashPolicy.Allow)
	assert.Equal(t, []string{"find"}, cfg.BashPolicy.Deny)
	assert.Equal(t, []string{"gitlab.example.com/api"}, cfg.BashPolicy.DenyArgs)
	assert.Equal(t, []string{"astro"}, cfg.BashPolicy.NPX)
	require.NotNil(t, cfg.Submit)
	assert.Equal(t, []string{"npm run lint", "npm run build"}, cfg.Submit.Gate)
	assert.Equal(t, []string{"bot/", "demo/"}, cfg.Submit.TaskBranchPrefixes())
	assert.Equal(t, 2*time.Minute, cfg.Submit.GateTimeout())
	assert.True(t, cfg.Submit.PushEnabled())
}

func TestUnknownKeysAreRejectedOnlyByStrictConfig(t *testing.T) {
	const body = "file_policy:\n  write_alow: [\"src/**\"]\n"

	strict := Default()
	require.Error(t, strict.LoadFromYAML(writeConfig(t, t.TempDir(), "strict_config: true\n"+body)))

	// Without strict_config the existing lenient behaviour is unchanged.
	lenient := Default()
	require.NoError(t, lenient.LoadFromYAML(writeConfig(t, t.TempDir(), body)))
	require.NotNil(t, lenient.FilePolicy)
	assert.Empty(t, lenient.FilePolicy.WriteAllow)
}

func TestStrictConfigFileRecognisesBrokenFiles(t *testing.T) {
	assert.True(t, StrictConfigFile(writeConfig(t, t.TempDir(), "strict_config: true\nbroken: [\n")))
	assert.False(t, StrictConfigFile(writeConfig(t, t.TempDir(), "file_policy: {}\n")))
	assert.False(t, StrictConfigFile(filepath.Join(t.TempDir(), "missing.yaml")))
}

func TestGuardProfileRejectsInvalidValues(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.md")
	require.NoError(t, os.WriteFile(empty, []byte("  \n"), 0o600))

	cases := map[string]string{
		"absolute pattern":      "file_policy:\n  write_allow: [\"/etc/**\"]\n",
		"parent pattern":        "file_policy:\n  write_allow: [\"src/../**\"]\n",
		"empty pattern":         "file_policy:\n  write_allow: [\" \"]\n",
		"program with path":     "bash_policy:\n  allow: [\"/bin/sh\"]\n",
		"gate required":         "submit:\n  gate: []\n",
		"branch prefix":         "submit:\n  gate: [\"true\"]\n  branch_prefixes: [\"bot\"]\n",
		"negative gate timeout": "submit:\n  gate: [\"true\"]\n  gate_timeout_seconds: -1\n",
		"incomplete mr":         "submit:\n  gate: [\"true\"]\n  mr:\n    api_url: \"https://gitlab.example.com/api/v4\"\n",
		"missing prompt":        "prompt_file: missing.md\n",
		"empty prompt":          "prompt_file: " + empty + "\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			assert.Error(t, cfg.LoadFromYAML(writeConfig(t, t.TempDir(), body)))
		})
	}
}

func TestSubmitDefaults(t *testing.T) {
	var submit SubmitConfig
	assert.Equal(t, []string{"bot/"}, submit.TaskBranchPrefixes())
	assert.Equal(t, 15*time.Minute, submit.GateTimeout())
	assert.True(t, submit.PushEnabled())
	push := false
	submit.Push = &push
	assert.False(t, submit.PushEnabled())
}
