package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ─── GET /sessions/{id}/diff ────────────────────────────────────────────────

func (s *Server) getSessionDiff(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.resolveSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	cwd := sess.Workspace()
	if cwd == "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{}})
		return
	}

	// Run git diff --stat and git diff in the workspace
	files, err := gitDiff(cwd)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "git diff failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
}

// ─── GET /sessions/{id}/file?path=... ───────────────────────────────────────

func (s *Server) getSessionFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	sess, err := s.resolveSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	safePath, err := securePath(sess.Workspace(), path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"content": string(data)})
}

// ─── PUT /sessions/{id}/file?path=... ────────────────────────────────────────

type writeFileRequest struct {
	Content string `json:"content"`
}

func (s *Server) writeFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	var req writeFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	sess, err := s.resolveSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	safePath, err := securePath(sess.Workspace(), path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Ensure parent directory exists
	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create directory: "+err.Error())
		return
	}

	if err := os.WriteFile(safePath, []byte(req.Content), 0644); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write file: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// gitDiff runs git diff in the given directory and returns parsed file diffs.
type fileDiff struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Patch   string `json:"patch"`
}

func gitDiff(cwd string) ([]fileDiff, error) {
	// Get the raw diff
	cmd := exec.Command("git", "diff", "--no-color")
	cmd.Dir = cwd
	output, err := cmd.Output()
	if err != nil {
		// git diff returns exit code 1 if there are differences in some configs
		// but with --no-color it should be 0. If it fails, return empty.
		return []fileDiff{}, nil
	}

	diffText := string(output)
	if diffText == "" {
		return []fileDiff{}, nil
	}

	// Parse diff into per-file sections
	var files []fileDiff
	var current *fileDiff
	var currentPatch strings.Builder

	for _, line := range strings.Split(diffText, "\n") {
		if strings.HasPrefix(line, "diff --git") {
			if current != nil {
				current.Patch = currentPatch.String()
				files = append(files, *current)
			}
			current = &fileDiff{}
			currentPatch.Reset()
			currentPatch.WriteString(line + "\n")
		} else if current != nil {
			currentPatch.WriteString(line + "\n")
			if strings.HasPrefix(line, "+++ b/") {
				current.Path = strings.TrimPrefix(line, "+++ b/")
			} else if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				current.Added++
			} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				current.Removed++
			}
		}
	}
	if current != nil {
		current.Patch = currentPatch.String()
		files = append(files, *current)
	}

	return files, nil
}

// ─── GET /workspace/list-dir?path=... ───────────────────────────────────────

// workspaceRoot uses the selected session's project when explicitly supplied.
// An explicitly selected missing or invalid session never falls back to the global workspace.
func (s *Server) workspaceRoot(w http.ResponseWriter, r *http.Request) (string, bool) {
	query := r.URL.Query()
	if !query.Has("session_id") {
		return s.app.Config().Workspace, true
	}
	id := query.Get("session_id")
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, "/\\") {
		writeError(w, http.StatusNotFound, "session not found")
		return "", false
	}
	manager := s.app.SessionManager()
	if _, err := securePath(manager.SessionsDir(), id); err != nil || !manager.Exists(id) {
		writeError(w, http.StatusNotFound, "session not found")
		return "", false
	}
	sess, err := s.app.LoadSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return "", false
	}
	return sess.Workspace(), true
}

// DirEntry represents a single entry in a directory listing.
type DirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

func (s *Server) listDir(w http.ResponseWriter, r *http.Request) {
	root, ok := s.workspaceRoot(w, r)
	if !ok {
		return
	}
	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = root
	}
	safePath, err := securePath(root, dirPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	entries, err := os.ReadDir(safePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read directory: "+err.Error())
		return
	}

	// Hidden directories that should never appear in the file explorer.
	hiddenDirs := map[string]bool{
		"node_modules": true, ".git": true, ".svn": true, ".hg": true,
		".DS_Store": true, "__pycache__": true, ".pytest_cache": true,
		"dist": true, "build": true, "out": true, ".next": true,
		".nuxt": true, ".turbo": true, ".cache": true, "vendor": true,
		"target": true, ".gradle": true, ".idea": true, ".vscode": true,
	}

	// Read the canonical directory for access control, but return paths in
	// the caller's workspace spelling. Otherwise a symlinked workspace (e.g.
	// /var on macOS) loses breadcrumbs and persisted editor tabs on the client.
	displayPath := dirPath
	if !filepath.IsAbs(displayPath) {
		displayPath = filepath.Join(root, displayPath)
	}
	var result []DirEntry
	for _, entry := range entries {
		name := entry.Name()

		// Skip hidden files/dirs (starting with .) except some common config files
		if strings.HasPrefix(name, ".") {
			// Allow .env, .gitignore, .golangci.yml, etc (dotfiles that are useful)
			// but skip .DS_Store
			if name == ".DS_Store" || name == ".git" || name == ".svn" || name == ".hg" {
				continue
			}
		}

		// Skip known heavy directories
		if hiddenDirs[name] {
			continue
		}

		isDir := entry.IsDir()
		result = append(result, DirEntry{
			Name:  name,
			Path:  filepath.Join(displayPath, name),
			IsDir: isDir,
		})
	}

	if result == nil {
		result = []DirEntry{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// ─── GET /workspace/search-files?path=... ────────────────────────────────────

func (s *Server) searchFiles(w http.ResponseWriter, r *http.Request) {
	root, ok := s.workspaceRoot(w, r)
	if !ok {
		return
	}
	rootPath := r.URL.Query().Get("path")
	if rootPath == "" {
		rootPath = root
	}
	safeRoot, err := securePath(root, rootPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Collect all file paths under root, respecting ignore patterns.
	var files []string

	hiddenDirs := map[string]bool{
		"node_modules": true, ".git": true, ".svn": true, ".hg": true,
		"__pycache__": true, ".pytest_cache": true, "dist": true,
		"build": true, "out": true, ".next": true, ".nuxt": true,
		".turbo": true, ".cache": true, "vendor": true, "target": true,
		".gradle": true, ".idea": true, ".vscode": true, ".DS_Store": true,
	}

	maxFiles := 20000 // safety limit to avoid scanning huge repos
	var walk func(dir string) bool
	walk = func(dir string) bool {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		for _, entry := range entries {
			name := entry.Name()
			if len(files) >= maxFiles {
				return true // stop
			}

			// Skip hidden and known heavy directories
			if hiddenDirs[name] {
				continue
			}
			if strings.HasPrefix(name, ".") && name != ".gitignore" && name != ".env" {
				continue
			}

			fullPath := filepath.Join(dir, name)
			relPath, err := filepath.Rel(safeRoot, fullPath)
			if err != nil {
				relPath = name
			}

			if entry.IsDir() {
				if walk(fullPath) {
					return true
				}
			} else {
				files = append(files, relPath)
			}
		}
		return false
	}

	_ = walk(safeRoot)

	if files == nil {
		files = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(files)
}

// ─── GET /workspace/read-file?path=... ───────────────────────────────────────

func (s *Server) workspaceReadFile(w http.ResponseWriter, r *http.Request) {
	root, ok := s.workspaceRoot(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	safePath, err := securePath(root, path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := os.ReadFile(safePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"content": string(data)})
}

// ─── GET /workspace/read-file-base64?path=... ────────────────────────────────

func (s *Server) workspaceReadFileBase64(w http.ResponseWriter, r *http.Request) {
	root, ok := s.workspaceRoot(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	safePath, err := securePath(root, path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := os.ReadFile(safePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found: "+err.Error())
		return
	}

	// Detect MIME type from extension
	ext := strings.ToLower(filepath.Ext(path))
	mimeType := "application/octet-stream"
	switch ext {
	case ".png":
		mimeType = "image/png"
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	case ".gif":
		mimeType = "image/gif"
	case ".webp":
		mimeType = "image/webp"
	case ".bmp":
		mimeType = "image/bmp"
	case ".svg":
		mimeType = "image/svg+xml"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"data":     base64.StdEncoding.EncodeToString(data),
		"mimeType": mimeType,
	})
}

// ─── PUT /workspace/write-file?path=... ──────────────────────────────────────

func (s *Server) workspaceWriteFile(w http.ResponseWriter, r *http.Request) {
	root, ok := s.workspaceRoot(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	var req writeFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	safePath, err := securePath(root, path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Ensure parent directory exists
	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create directory: "+err.Error())
		return
	}

	if err := os.WriteFile(safePath, []byte(req.Content), 0644); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write file: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
