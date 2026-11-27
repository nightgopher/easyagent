package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaxOutputTokensIsLoadedFromYAMLAndEnv(t *testing.T) {
	// 默认沿用模型表：只有部署显式配置时才覆盖。
	assert.Zero(t, Default().MaxOutputTokens)

	cfg := Default()
	require.NoError(t, cfg.LoadFromYAML(writeConfig(t, t.TempDir(), "max_output_tokens: 32768\n")))
	assert.Equal(t, 32768, cfg.MaxOutputTokens)

	t.Setenv("EA_MAX_OUTPUT_TOKENS", "16384")
	fromEnv := Default()
	fromEnv.LoadFromEnv()
	assert.Equal(t, 16384, fromEnv.MaxOutputTokens)
}
