package feishu

import (
	"strings"
	"testing"
)

func TestBuiltinAppSecretRequiresEnvironment(t *testing.T) {
	t.Setenv("FEISHU_BUILTIN_APP_SECRET", "")
	if _, err := StartOAuthFlow(t.Context()); err == nil || !strings.Contains(err.Error(), "FEISHU_BUILTIN_APP_SECRET") {
		t.Fatalf("expected missing FEISHU_BUILTIN_APP_SECRET error, got %v", err)
	}
}
