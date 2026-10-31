package server

import "net/http"

func (s *Server) publishAgentMemory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SourceID  string `json:"source_id"`
		Directory string `json:"directory"`
		Title     string `json:"title"`
		Content   string `json:"content"`
		Version   int64  `json:"version"`
	}
	if !decodeAgentJSON(w, r, &input) {
		return
	}
	store := s.app.Agents()
	name, err := store.PublishMemory(r.Context(), r.PathValue("agent"), r.PathValue("memory"), input.SourceID, input.Directory, input.Title, input.Content, input.Version)
	if err != nil {
		agentDataError(w, err)
		return
	}
	// A committed file must never be reported as a failed publication. Indexing
	// can be retried separately, and normal searches synchronize it too.
	result := map[string]any{"source_id": input.SourceID, "path": name, "saved": true}
	if err := store.IndexSource(r.Context(), input.SourceID); err != nil {
		result["warning"] = "文档已保存；索引暂未更新，请在知识来源中重建索引。"
	}
	writeJSON(w, result)
}
