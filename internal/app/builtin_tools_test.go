package app

import (
	"errors"
	"testing"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/stretchr/testify/require"
)

func TestBuiltinToolCommandLineFlagCannotBeOverriddenBySettings(t *testing.T) {
	a := taskTestApp(t, nil)
	a.cfg.MarkBuiltinToolCommandLine("web_search")
	require.True(t, a.Config().EnableWebSearch)
	err := a.SetBuiltinToolSetting("web_search", false)
	require.True(t, errors.Is(err, config.ErrBuiltinToolCommandLine))
	require.True(t, a.Config().EnableWebSearch)
}
