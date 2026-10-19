package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/hwj123hwj/easyagent/sdk/agent"
	"github.com/hwj123hwj/easyagent/sdk/runtime"
)

type forkRequest struct {
	Application        *string `json:"application,omitempty"`
	EntryID            *string `json:"entry_id,omitempty"`
	BeforeMessageIndex *int    `json:"before_message_index,omitempty"`
}
type forkResponse struct {
	ID                 string `json:"id"`
	SourceID           string `json:"source_id"`
	KeptMessages       int    `json:"kept_messages"`
	KeptUserMessages   int    `json:"kept_user_messages"`
	DroppedMessages    int    `json:"dropped_messages"`
	BranchUserMsgIndex int    `json:"branch_user_message_index"`
}

func (s *Server) forkSession(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("id")
	var req forkRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, 400, "invalid fork request")
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, 400, "invalid fork request")
		return
	}
	if req.EntryID != nil && req.BeforeMessageIndex != nil {
		writeError(w, 400, "select one fork point")
		return
	}
	// Share admission lock with prompts/deletion. Runtime Fork also protects
	// writes from other entrypoints and manual compaction.
	s.runs.mu.Lock()
	defer s.runs.mu.Unlock()
	if req.Application != nil && *req.Application != "" && *req.Application != "music" && *req.Application != "kb" {
		writeError(w, 400, "unknown application")
		return
	}
	if state := s.runs.sessions[sourceID]; state != nil && (state.admission != nil || sessionBusy(state) && req.EntryID == nil && req.BeforeMessageIndex == nil) {
		writeError(w, 409, "当前轮次完成后可分叉；运行中请选择更早的历史消息")
		return
	}
	source, err := s.app.LoadSession(r.Context(), sourceID)
	if err != nil {
		writeError(w, 404, "session not found")
		return
	}
	result, err := source.ForkWithOptions(r.Context(), runtime.ForkOptions{
		EntryID: req.EntryID, BeforeMessageIndex: req.BeforeMessageIndex, Application: req.Application,
	})
	if errors.Is(err, agent.ErrAgentBusy) {
		writeError(w, 409, "当前轮次完成后可分叉；运行中请选择更早的历史消息")
		return
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(forkResponse{ID: result.ID, SourceID: sourceID, KeptMessages: result.KeptMessages, KeptUserMessages: result.KeptUserMessages, DroppedMessages: result.DroppedMessages, BranchUserMsgIndex: result.KeptUserMessages})
}
