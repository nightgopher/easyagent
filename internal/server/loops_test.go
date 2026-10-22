package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionLoopEndpoints(t *testing.T) {
	application := newTestApp(t)
	sess, err := application.LoadOrCreateSession(context.Background(), "")
	require.NoError(t, err)
	srv := New(application, nil)
	call := func(method string) map[string]any {
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, localReq(method, "/sessions/"+sess.SessionID()+"/loop", nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		return body
	}
	require.Nil(t, call(http.MethodGet)["loop"])

	_, err = application.LoopManager().Start(sess.SessionID(), "检查部署", 15*time.Minute, time.Hour, func(context.Context, string) error { return nil })
	require.NoError(t, err)
	loop := call(http.MethodGet)["loop"].(map[string]any)
	require.Equal(t, "检查部署", loop["prompt"])
	require.Equal(t, float64(900), loop["interval_seconds"])
	require.NotContains(t, loop, "last_run_at", "a loop that never ran has no last run")

	require.Equal(t, true, call(http.MethodDelete)["stopped"])
	require.Nil(t, call(http.MethodGet)["loop"])
	require.Equal(t, false, call(http.MethodDelete)["stopped"])

	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, localReq(http.MethodGet, "/sessions/missing/loop", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}
