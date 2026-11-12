package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// steer POST /sessions/{id}/steer：
//   - 空闲会话 → 409 not_running（客户端应回退普通消息）；
//   - 未知会话 → 404；空 prompt → 400；
//   - 运行中 → 200 steered，插话作为最后一次请求的最后一条 user message 进入模型。
func TestSteerEndpoint(t *testing.T) {
	srv, _, gateway := newRunTestServer(t)
	sess, err := srv.app.NewSession(context.Background())
	require.NoError(t, err)
	handler := srv.Handler()

	post := func(sessionID, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/sessions/"+sessionID+"/steer", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234" // 回环地址免鉴权；httptest 默认非回环会被 401。
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 空闲会话：拒绝插话，客户端可据此回退为普通消息。
	rec := post(sess.SessionID(), `{"prompt":"hello"}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "not_running")

	// 未知会话：404。
	rec = post("no-such-session", `{"prompt":"hello"}`)
	require.Equal(t, http.StatusNotFound, rec.Code)

	// 空 prompt：400。
	rec = post(sess.SessionID(), `{"prompt":"  "}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// 运行中：第一次模型请求已在 provider 侧阻塞。
	run, _, err := srv.startRun(sess.SessionID(), "long task", "steer-run-1")
	require.NoError(t, err)
	<-gateway.requests

	rec = post(sess.SessionID(), `{"prompt":"also do X"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"steered":true`)

	close(gateway.finish)
	_, err = srv.waitRun(context.Background(), run)
	require.NoError(t, err)

	require.EqualValues(t, 2, gateway.calls.Load(), "steering must trigger exactly one more model request")
	second := <-gateway.requests
	messages := second["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	require.Equal(t, "user", last["role"])
	require.Contains(t, fmt.Sprintf("%v", last["content"]), "also do X")
}
