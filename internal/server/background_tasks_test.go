package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	basetools "github.com/hwj123hwj/easyagent/sdk/tools"
	"github.com/stretchr/testify/require"
)

func TestBackgroundTaskEndpoints(t *testing.T) {
	application := newTestApp(t)
	sess, err := application.LoadOrCreateSession(context.Background(), "")
	require.NoError(t, err)
	holder, ok := sess.Ext().(interface {
		BackgroundTasks() *basetools.BackgroundTasks
	})
	require.True(t, ok, "coding sessions own a background task registry")
	tasks := holder.BackgroundTasks()
	t.Cleanup(func() { _ = tasks.Close() })
	bash := basetools.NewBashTool(basetools.WithBashBackground(tasks), basetools.WithBashWorkspace(t.TempDir()))
	_, err = bash.Execute(context.Background(), json.RawMessage(`{"command":"echo started; sleep 30","run_in_background":true}`), nil)
	require.NoError(t, err)

	srv := New(application, nil)
	call := func(method, path string) map[string]any {
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, localReq(method, path, nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		return body
	}
	base := "/sessions/" + sess.SessionID() + "/background-tasks"
	list := call(http.MethodGet, base)["tasks"].([]any)
	require.Len(t, list, 1)
	task := list[0].(map[string]any)
	require.Equal(t, "bg-1", task["id"])
	require.Equal(t, true, task["running"])

	require.Eventually(t, func() bool {
		return strings.Contains(call(http.MethodGet, base+"/bg-1")["output"].(string), "started")
	}, 5*time.Second, 20*time.Millisecond)
	// Viewing output must not consume what bash_output returns to the model.
	content, _ := basetools.NewBashOutputTool(tasks, 0).Execute(context.Background(), json.RawMessage(`{"task_id":"bg-1"}`), nil)
	require.Contains(t, content.Content, "started")

	require.Equal(t, true, call(http.MethodPost, base+"/bg-1/stop")["stopped"])
	stopped := call(http.MethodGet, base)["tasks"].([]any)[0].(map[string]any)
	require.Equal(t, false, stopped["running"])
	require.Equal(t, "killed", stopped["status"])

	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, localReq(http.MethodPost, base+"/bg-9/stop", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}
