package server

import (
	"archive/zip"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nightgopher/easyagent/internal/export"
)

const exportMaxBatchSessions = 50

// exportSession 处理 GET /sessions/{id}/export?format=md|json|txt&tools=all|none:
// 把单个会话渲染为附件下载。
func (s *Server) exportSession(w http.ResponseWriter, r *http.Request) {
	format, includeTools := exportQueryOptions(r)
	sess, err := s.resolveSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	messages, err := sess.Session().BuildDisplayContext(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	opts := export.Options{
		Format:       export.Format(format),
		IncludeTools: includeTools,
		SessionID:    sess.SessionID(),
		Title:        s.sessionExportTitle(sess.SessionID(), r),
	}
	result, err := export.Render(messages, opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(result.Filename)))
	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("Content-Length", fmt.Sprint(len(result.Content)))
	_, _ = w.Write(result.Content)
}

// exportSessionsZip 处理 GET /sessions/export?ids=a,b,c&format=md&tools=all:
// 批量渲染多个会话并打包为 zip 下载。
func (s *Server) exportSessionsZip(w http.ResponseWriter, r *http.Request) {
	rawIDs := strings.Split(r.URL.Query().Get("ids"), ",")
	var ids []string
	seen := map[string]bool{}
	for _, id := range rawIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "require ids query parameter")
		return
	}
	if len(ids) > exportMaxBatchSessions {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("too many sessions (max %d)", exportMaxBatchSessions))
		return
	}
	format, includeTools := exportQueryOptions(r)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(exportZipName())))
	zipWriter := zip.NewWriter(w)
	defer func() { _ = zipWriter.Close() }()

	var missing []string
	usedNames := map[string]bool{}
	for _, id := range ids {
		if !s.app.SessionManager().Exists(id) {
			missing = append(missing, id)
			continue
		}
		sess, err := s.resolveSession(r.Context(), id)
		if err != nil {
			missing = append(missing, id)
			continue
		}
		messages, err := sess.Session().BuildDisplayContext(r.Context())
		if err != nil {
			missing = append(missing, id)
			continue
		}
		result, err := export.Render(messages, export.Options{
			Format:       export.Format(format),
			IncludeTools: includeTools,
			SessionID:    sess.SessionID(),
			Title:        s.sessionExportTitle(sess.SessionID(), r),
		})
		if err != nil {
			missing = append(missing, id)
			continue
		}
		name := result.Filename
		for usedNames[name] {
			name = strings.TrimSuffix(name, "."+format) + "-1." + format
		}
		usedNames[name] = true
		entry, err := zipWriter.Create(name)
		if err != nil {
			return
		}
		if _, err := entry.Write(result.Content); err != nil {
			return
		}
	}
	if len(missing) > 0 {
		entry, err := zipWriter.Create("_导出说明.txt")
		if err == nil {
			_, _ = fmt.Fprintf(entry, "以下会话未找到或无法导出:\n%s\n", strings.Join(missing, "\n"))
		}
	}
}

// exportQueryOptions 解析 format 与 tools 查询参数。format 默认 md;
// tools 默认 all,显式传 none 表示不含工具调用。
func exportQueryOptions(r *http.Request) (format string, includeTools bool) {
	format = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if !export.ValidFormat(format) {
		format = string(export.FormatMarkdown)
	}
	includeTools = r.URL.Query().Get("tools") != "none"
	return format, includeTools
}

// sessionExportTitle 查会话标题(meta.json 的 title),查不到回退会话 ID。
func (s *Server) sessionExportTitle(sessionID string, r *http.Request) string {
	sessions, err := s.app.SessionManager().List(r.Context())
	if err != nil {
		return sessionID
	}
	for _, info := range sessions {
		if info.ID == sessionID && strings.TrimSpace(info.Title) != "" {
			return info.Title
		}
	}
	return sessionID
}

// exportZipName 批量导出的 zip 文件名。
func exportZipName() string {
	return fmt.Sprintf("easyagent-sessions-%s.zip", time.Now().Format("20060102-150405"))
}
