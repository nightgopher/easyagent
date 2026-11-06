package coding

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/stretchr/testify/require"
)

func TestReadProtectionSurvivesToolRebuildAndIsolatesSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(path, []byte("initial"), 0o600))
	app := CodingApplication{}
	ext := app.NewSessionExt()
	opts := runtime.ToolBuildOptions{Workspace: dir, SessionID: "session", SessionExt: ext}
	find := func(list []agent.Tool, name string) agent.Tool {
		for _, tool := range list {
			if tool.Name() == name {
				return tool
			}
		}
		t.Fatalf("missing tool %s", name)
		return nil
	}
	readArgs, err := json.Marshal(map[string]string{"path": path})
	require.NoError(t, err)
	_, err = find(app.BuildTools(opts), "read").Execute(context.Background(), readArgs, nil)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("external"), 0o600))
	writeArgs, err := json.Marshal(map[string]string{"path": path, "content": "replacement"})
	require.NoError(t, err)
	_, err = find(app.BuildTools(opts), "write").Execute(context.Background(), writeArgs, nil)
	require.ErrorContains(t, err, "modified externally")
	// Even the same persisted ID must not retain a discarded live session's state.
	opts.SessionExt = app.NewSessionExt()
	_, err = find(app.BuildTools(opts), "write").Execute(context.Background(), writeArgs, nil)
	require.NoError(t, err)
}

func TestDefaultWebToolsAreAvailableAndRespectFilters(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	app := NewCodingApplication(cfg)
	opts := runtime.ToolBuildOptions{Workspace: t.TempDir()}
	names := func(list []agent.Tool) []string {
		result := make([]string, 0, len(list))
		for _, tool := range list {
			result = append(result, tool.Name())
		}
		return result
	}
	require.Contains(t, names(app.BuildTools(opts)), "web_search")
	require.Contains(t, names(app.BuildTools(opts)), "web_fetch")
	require.Contains(t, app.ToolNames(false), "web_search")
	require.Contains(t, app.ToolNames(false), "web_fetch")
	opts.BlockedTools = []string{"web_search", "web_fetch"}
	require.NotContains(t, names(app.BuildTools(opts)), "web_search")
	require.NotContains(t, names(app.BuildTools(opts)), "web_fetch")
	cfg.EnableWeb, cfg.EnableWebSearch = false, false
	app = NewCodingApplication(cfg)
	opts.BlockedTools = nil
	require.NotContains(t, names(app.BuildTools(opts)), "web_search")
	require.NotContains(t, names(app.BuildTools(opts)), "web_fetch")
	require.NotContains(t, app.ToolNames(false), "web_search")
	require.NotContains(t, app.ToolNames(false), "web_fetch")
}
