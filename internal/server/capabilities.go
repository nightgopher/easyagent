package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/hwj123hwj/easyagent/sdk/agent"
	"github.com/hwj123hwj/easyagent/sdk/runtime"
	"github.com/hwj123hwj/easyagent/sdk/skill"
)

// pathWithin reports whether target is root itself or lies below it.
func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *Server) getCapabilities(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.app.SessionManager().Exists(id) {
		writeError(w, 404, "session not found")
		return
	}
	sess, err := s.resolveSession(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// A project skill with the same name as a global one wins over it; say so.
	global := map[string]string{}
	for _, g := range skill.LoadFromDirs(s.app.GlobalSkillDirs()...).Skills {
		global[g.Name] = g.FilePath
	}
	// When the workspace is the home directory its skill roots are the personal ones, which apply
	// to every project, so they are not "this project's" skills.
	personalDirs := map[string]bool{}
	for _, dir := range runtime.PersonalSkillDirs() {
		personalDirs[filepath.Clean(dir)] = true
	}
	projectDirs := []string{}
	for _, dir := range runtime.ProjectSkillDirs(sess.Workspace()) {
		if !personalDirs[filepath.Clean(dir)] {
			projectDirs = append(projectDirs, dir)
		}
	}
	skills := []map[string]any{}
	for _, loaded := range sess.LoadedSkills() {
		scope, origin := "personal", ""
		for _, dir := range projectDirs {
			if pathWithin(dir, loaded.FilePath) {
				scope, origin = "project", skillRootOrigin(dir)
				break
			}
		}
		entry := map[string]any{"name": loaded.Name, "description": loaded.Description, "source": loaded.FilePath, "scope": scope, "model_invocation": !loaded.DisableModelInvocation}
		if scope == "project" {
			entry["origin"] = origin
			if overridden, ok := global[loaded.Name]; ok {
				entry["overrides"] = overridden
			}
		}
		skills = append(skills, entry)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"workspace": sess.Workspace(), "skills": skills, "tools": sess.ToolNames()})
}

// refreshSessionSkills serves POST /sessions/{id}/skills/refresh: re-scan the skill directories so
// skills added or edited since the session started apply from the next prompt, without starting a
// new session. A running task keeps the tool set it started with, so this waits until it is done.
func (s *Server) refreshSessionSkills(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.app.SessionManager().Exists(id) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	sess, err := s.resolveSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := sess.RefreshTools(r.Context()); err != nil {
		if errors.Is(err, agent.ErrAgentBusy) {
			writeError(w, http.StatusConflict, "会话仍在执行任务，请等它完成后再刷新技能")
			return
		}
		writeError(w, http.StatusInternalServerError, "refresh skills failed: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"skills": len(sess.LoadedSkills())})
}
