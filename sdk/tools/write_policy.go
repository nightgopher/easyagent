package tools

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// ErrPathEscapesWorkspace is returned when a path resolves outside the session workspace.
var ErrPathEscapesWorkspace = errors.New("path escapes workspace")

// WritePolicy is the workspace-relative allowlist of the file mutation tools.
// A `**` segment matches any number of path segments, `*` and `?` match inside
// one segment, and a pattern ending in `/**` also matches the directory itself.
type WritePolicy struct {
	Allow []string
	Deny  []string
}

// Configured reports whether a write policy restricts mutations at all.
func (p WritePolicy) Configured() bool { return len(p.Allow) > 0 || len(p.Deny) > 0 }

// Check reports whether name, a clean slash-separated workspace-relative path,
// may be changed. Deny patterns win; without allow patterns everything else passes.
func (p WritePolicy) Check(name string) error {
	for _, pattern := range p.Deny {
		if MatchPathGlob(pattern, name) {
			return fmt.Errorf("path %s matches the protected pattern %q", name, pattern)
		}
	}
	if len(p.Allow) == 0 {
		return nil
	}
	for _, pattern := range p.Allow {
		if MatchPathGlob(pattern, name) {
			return nil
		}
	}
	return fmt.Errorf("path %s is outside the writable paths of this workspace (allowed: %s)", name, strings.Join(p.Allow, ", "))
}

// MatchPathGlob reports whether a slash-separated path matches a workspace glob.
func MatchPathGlob(pattern, name string) bool {
	return matchGlobSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchGlobSegments(pattern, name []string) bool {
	if len(pattern) > 0 && pattern[0] == "**" {
		if len(pattern) == 1 {
			return true
		}
		for skip := 0; skip <= len(name); skip++ {
			if matchGlobSegments(pattern[1:], name[skip:]) {
				return true
			}
		}
		return false
	}
	if len(pattern) == 0 || len(name) == 0 {
		return len(pattern) == len(name)
	}
	matched, err := path.Match(pattern[0], name[0])
	if err != nil || !matched {
		return false
	}
	return matchGlobSegments(pattern[1:], name[1:])
}

// CheckWrite reports whether a mutation tool may change path: the path must
// stay inside the workspace and, once a WritePolicy is configured, be one of
// its writable paths. The judgement uses the symlink-resolved location, so a
// link cannot lend a writable name to a protected file.
func (p PathPolicy) CheckWrite(workspace, target string) error {
	if !p.Allows(workspace, target) {
		return ErrPathEscapesWorkspace
	}
	if !p.Configured() || workspace == "" {
		return nil
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		root = filepath.Clean(workspace)
	}
	rel, err := filepath.Rel(root, realPath(target))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrPathEscapesWorkspace
	}
	return p.Check(filepath.ToSlash(rel))
}

// describeWriteDenied keeps the workspace message of an escaped path and adds
// the policy's reason for a protected one.
func describeWriteDenied(requested, workspace string, err error) string {
	if errors.Is(err, ErrPathEscapesWorkspace) {
		return fmt.Sprintf("path %s is outside workspace %s", requested, workspace)
	}
	return fmt.Sprintf("path %s was not changed: %v", requested, err)
}

// realPath resolves the symlinks of an existing path, or of the nearest
// existing ancestor of a path that does not exist yet.
func realPath(target string) string {
	resolved, tail := target, ""
	for {
		if real, err := filepath.EvalSymlinks(resolved); err == nil {
			return filepath.Join(real, tail)
		}
		parent := filepath.Dir(resolved)
		if parent == resolved {
			return target
		}
		tail = filepath.Join(filepath.Base(resolved), tail)
		resolved = parent
	}
}
