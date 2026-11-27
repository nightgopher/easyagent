package app

import (
	"fmt"
	"path/filepath"
)

// checkProjectSession keeps a project-scoped deployment tied to its configured
// workspace and application, including when old sessions are restored.
func (a *App) checkProjectSession(workspace, application, agentID string) error {
	cfg := a.Config()
	if cfg.ContextScope != "project" {
		return nil
	}
	if agentID != "" || application != "" && application != "coding" {
		return fmt.Errorf("project-scoped deployment only supports its configured coding expert")
	}
	if cfg.Workspace == "" {
		return fmt.Errorf("project-scoped deployment requires a workspace")
	}
	root, err := filepath.EvalSymlinks(cfg.Workspace)
	if err != nil {
		return fmt.Errorf("project workspace: %w", err)
	}
	if workspace == "" {
		return nil
	}
	selected, err := filepath.EvalSymlinks(workspace)
	if err != nil || !filepath.IsAbs(workspace) || filepath.Clean(selected) != filepath.Clean(root) {
		return fmt.Errorf("session workspace does not match the configured expert workspace")
	}
	return nil
}

// ResolveProjectWorkspace validates the workspace and identity selected by a
// session entrypoint before it can override the deployment's configuration.
func (a *App) ResolveProjectWorkspace(workspace, application, agentID string) error {
	return a.checkProjectSession(workspace, application, agentID)
}
