package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 看门狗经 POST /chat 以 channel=larkwatch 投递任务；核心必须接受该渠道，
// 否则注入全部 400（v0.46.2 首发时曾因白名单缺失导致看门狗静默失效）。
func TestLarkwatchChannelAcceptedForChat(t *testing.T) {
	srv, server, gateway := newRunTestServer(t)
	close(gateway.finish)
	request := func(channel, requestID string) (int, map[string]any) {
		body := `{"prompt":"hi","channel":"` + channel + `","request_id":"` + requestID + `"}`
		req, err := http.NewRequest(http.MethodPost, server.URL+"/chat", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var data map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&data))
		return resp.StatusCode, data
	}

	status, data := request("larkwatch", "larkwatch-t1")
	require.Equal(t, http.StatusOK, status)
	require.NotEmpty(t, data["session_id"])

	// 未知渠道依旧拒绝，白名单收紧行为不变。
	status, _ = request("bogus-channel", "larkwatch-t2")
	require.Equal(t, http.StatusBadRequest, status)

	_ = srv
}
