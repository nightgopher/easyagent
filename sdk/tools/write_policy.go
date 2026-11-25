package tools

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
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

// CheckWrite reports whether a mutation tool may change target. A configured
// policy requires an existing absolute workspace directory, checks both logical
// and physical paths, and rejects symlinks beneath the workspace and multiply
// linked regular files. AllowOutsideWorkspace does not override this policy.
// These are preflight checks, not an atomic filesystem sandbox: another process
// can still replace a path component between this check and the mutation.
func (p PathPolicy) CheckWrite(workspace, target string) error {
	if !p.Configured() {
		if !p.Allows(workspace, target) {
			return ErrPathEscapesWorkspace
		}
		return nil
	}
	if !filepath.IsAbs(workspace) {
		return fmt.Errorf("write policy requires an absolute workspace directory")
	}
	workspace = filepath.Clean(workspace)
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return fmt.Errorf("cannot resolve workspace: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("cannot inspect workspace: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("write policy requires a workspace directory")
	}
	target = ResolvePath(workspace, target)
	rel, err := workspaceRelative(workspace, target)
	if err != nil {
		return err
	}
	if err := p.Check(filepath.ToSlash(rel)); err != nil {
		return err
	}
	physical, err := realPath(target)
	if err != nil {
		return fmt.Errorf("cannot resolve mutation path: %w", err)
	}
	physicalRel, err := workspaceRelative(root, physical)
	if err != nil {
		return err
	}
	if err := p.Check(filepath.ToSlash(physicalRel)); err != nil {
		return err
	}
	// Resolve the workspace's own aliases (e.g. macOS /var), but do not
	// permit any symlink component inside it, even if it points at allowed data.
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil // All remaining components will be newly created.
		}
		if err != nil {
			return fmt.Errorf("cannot inspect mutation path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("mutation path traverses symlink %s", current)
		}
		if !info.IsDir() {
			if current != filepath.Join(root, rel) || !info.Mode().IsRegular() {
				return fmt.Errorf("mutation path is not a regular file or directory: %s", current)
			}
			if !singleLink(info) {
				return fmt.Errorf("mutation path has multiple hardlinks or an unverifiable link count: %s", current)
			}
		}
	}
	return nil
}

func workspaceRelative(root, target string) (string, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrPathEscapesWorkspace
	}
	return rel, nil
}

// singleLink reads the Unix stat link count without tying this file to a
// platform-specific stat type. os.SameFile can compare two known names but
// cannot find aliases outside the workspace. Fail closed on other stat layouts.
func singleLink(info os.FileInfo) bool {
	stat := reflect.ValueOf(info.Sys())
	if stat.Kind() != reflect.Pointer || stat.IsNil() || stat.Elem().Kind() != reflect.Struct {
		return false
	}
	nlink := stat.Elem().FieldByName("Nlink")
	return nlink.IsValid() && nlink.CanUint() && nlink.Uint() == 1
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
func realPath(target string) (string, error) {
	resolved, tail := target, ""
	for {
		if _, err := os.Lstat(resolved); err == nil {
			real, err := filepath.EvalSymlinks(resolved)
			if err != nil {
				return "", err // Do not skip dangling or looping symlinks.
			}
			return filepath.Join(real, tail), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(resolved)
		if parent == resolved {
			return "", fmt.Errorf("no existing ancestor for %s", target)
		}
		tail = filepath.Join(filepath.Base(resolved), tail)
		resolved = parent
	}
}
