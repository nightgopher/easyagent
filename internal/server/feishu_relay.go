package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nightgopher/easyagent/internal/feishu"
)

// feishuRelay 把看门狗托付的任务交给进程内飞书桥接：结果以扫码绑定机器人
// 的流式卡片发给主人，而不是由看门狗用另一个应用发纯文本。桥接未在本进程
// 运行时返回 503 bridge_unavailable，看门狗据此退回旧的转达方式。
func (s *Server) feishuRelay(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	relay := s.localBridge.relayer()
	if relay == nil {
		writeCodedError(w, http.StatusServiceUnavailable, "bridge_unavailable", "飞书桥接未在本进程运行")
		return
	}
	var req feishu.RelayRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeCodedError(w, http.StatusBadRequest, "invalid_request", "请求格式无效")
		return
	}
	result, err := relay(r.Context(), req)
	switch {
	case errors.Is(err, feishu.ErrRelayUnavailable):
		writeCodedError(w, http.StatusServiceUnavailable, "bridge_unavailable", "飞书桥接没有可投递的对话（主人未配对）")
	case errors.Is(err, feishu.ErrRelayNotAdmitted):
		writeCodedError(w, http.StatusConflict, "not_admitted", err.Error())
	case err != nil:
		writeCodedError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeJSON(w, result)
	}
}

func writeCodedError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}
