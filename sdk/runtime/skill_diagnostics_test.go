package runtime

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/nightgopher/easyagent/sdk/skill"
)

// Repeated agent rebuilds (model switches, tool refreshes) reload skills and
// re-emit the same load diagnostics; each unique diagnostic must be logged at
// most once per session so TUI and log files are not spammed.
func TestLogSkillDiagnosticsOnce_Deduplicates(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := &AgentSession{}
	diags := []skill.Diagnostic{
		{Code: skill.DiagInvalidName, Message: "name too long", Path: "/skills/a"},
		{Code: skill.DiagInvalidName, Message: "name too long", Path: "/skills/a"}, // same → suppressed
		{Code: skill.DiagParseFailed, Message: "bad frontmatter", Path: "/skills/b"},
	}
	logSkillDiagnosticsOnce(s, diags)
	logSkillDiagnosticsOnce(s, diags) // full repeat → fully suppressed

	got := buf.String()
	if n := strings.Count(got, "skill diagnostic"); n != 2 {
		t.Fatalf("expected 2 unique diagnostics logged, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "name too long") || !strings.Contains(got, "bad frontmatter") {
		t.Fatalf("missing expected messages:\n%s", got)
	}

	// A different path with the same message is a distinct diagnostic.
	buf.Reset()
	logSkillDiagnosticsOnce(s, []skill.Diagnostic{
		{Code: skill.DiagInvalidName, Message: "name too long", Path: "/skills/other"},
	})
	if n := strings.Count(buf.String(), "skill diagnostic"); n != 1 {
		t.Fatalf("distinct path should be logged, got %d:\n%s", n, buf.String())
	}
}
