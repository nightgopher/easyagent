package runtime

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGlobalSkillDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agents := filepath.Join(home, ".agents", "skills")
	claude := filepath.Join(home, ".claude", "skills")

	assert.Equal(t, []string{agents, claude}, PersonalSkillDirs())
	assert.Equal(t, []string{agents, claude}, GlobalSkillDirs(nil, ""), "defaults to the personal roots")
	assert.Equal(t, []string{agents, claude, "/market"}, GlobalSkillDirs(nil, "/market"), "the market directory is appended")
	assert.Equal(t, []string{"/custom", "/market"}, GlobalSkillDirs([]string{"/custom"}, "/market"), "explicit directories replace the defaults")

	explicit := []string{"/custom"}
	GlobalSkillDirs(explicit, "/market")
	assert.Equal(t, []string{"/custom"}, explicit, "the caller's slice is not modified")
}

func TestDefaultSkillDirsKeepsProjectBeforePersonal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := DefaultSkillDirs("/work/proj")
	assert.Equal(t, []string{
		"/work/proj/.agents/skills", "/work/proj/.claude/skills",
		filepath.Join(home, ".agents", "skills"), filepath.Join(home, ".claude", "skills"),
	}, got, "project definitions must win over personal ones with the same name")
}
