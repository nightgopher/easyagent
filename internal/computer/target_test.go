package computer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOldHelperNeverReceivesControl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell; native computer use supports macOS")
	}
	for _, version := range []string{`{"protocol_version":1}`, `{}`, `invalid`, `{"protocol_version":2}`} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "helper")
			marker := filepath.Join(dir, "called")
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%s' '" + version + "'; exit 0; fi\nprintf called > '" + marker + "'\nprintf '%s' '{\"success\":true}'\n"
			require.NoError(t, os.WriteFile(path, []byte(script), 0700))
			_, err := runHelperAtPath(context.Background(), path, "perform_action", map[string]any{"pid": 100, "window_id": 200})
			if version == `{"protocol_version":2}` {
				require.NoError(t, err)
				_, statErr := os.Stat(marker)
				require.NoError(t, statErr)
			} else {
				require.Error(t, err)
				_, statErr := os.Stat(marker)
				require.True(t, os.IsNotExist(statErr), "legacy helper must never receive a tool invocation")
			}
		})
	}
}
