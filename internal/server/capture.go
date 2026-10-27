package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgopher/easyagent/internal/capture"
)

func (s *Server) listCaptures(w http.ResponseWriter, r *http.Request) {
	records, err := capture.New(s.app.Config().DataDir).List()
	if err != nil {
		writeError(w, 500, "cannot read inbox")
		return
	}
	writeJSON(w, records)
}
func (s *Server) saveCapture(w http.ResponseWriter, r *http.Request) {
	var record capture.Record
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&record); err != nil {
		writeError(w, 400, "invalid record")
		return
	}
	if !capture.ValidID(record.ID) || strings.TrimSpace(record.Text) == "" || len(record.Text) > capture.MaxText || len(record.Source) > 2048 {
		writeError(w, 400, "invalid record")
		return
	}
	saved, err := capture.New(s.app.Config().DataDir).Save(record)
	if errors.Is(err, capture.ErrConflict) {
		writeError(w, 409, err.Error())
		return
	}
	if err != nil {
		writeError(w, 500, "cannot save record")
		return
	}
	writeJSON(w, saved)
}

// saveArtifact only creates a Markdown document; editing existing documents uses
// the ordinary workspace editor. os.Root confines symlink traversal during IO.
func (s *Server) saveArtifact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&req); err != nil {
		writeError(w, 400, "invalid artifact")
		return
	}
	if !filepath.IsLocal(req.Path) || !strings.EqualFold(filepath.Ext(req.Path), ".md") || strings.TrimSpace(req.Content) == "" || len(req.Content) > 1<<20 {
		writeError(w, 400, "require a relative Markdown path and content up to 1 MiB")
		return
	}
	sess, err := s.resolveSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "session not found")
		return
	}
	root, err := os.OpenRoot(sess.Workspace())
	if err != nil {
		writeError(w, 400, "workspace unavailable")
		return
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(req.Path), 0755); err != nil {
		writeError(w, 400, "invalid output directory")
		return
	}
	// Write and sync a staging file before publishing it without replacement.
	temp := filepath.Join(filepath.Dir(req.Path), ".artifact-"+capture.NewID())
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		writeError(w, 400, "cannot create artifact")
		return
	}
	defer func() { _ = root.Remove(temp) }()
	_, err = f.WriteString(req.Content)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		writeError(w, 500, "cannot save artifact")
		return
	}
	if err = root.Link(temp, req.Path); errors.Is(err, os.ErrExist) {
		writeError(w, 409, "文件已存在，请换一个名称；原文件未修改")
		return
	} else if err != nil {
		writeError(w, 400, "cannot publish artifact")
		return
	}
	writeJSON(w, map[string]string{"path": filepath.Join(sess.Workspace(), req.Path)})
}
