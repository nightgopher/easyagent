package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/operations"
)

// PatchTool applies validated unified diffs through the configured file backend.
type PatchTool struct {
	pathPolicy    PathPolicy
	workspace     string
	ops           operations.FileOperations
	backupMgr     *BackupManager
	mutationQueue MutationQueue
	readTracker   *ReadTracker
}

type PatchParams struct {
	PatchText string `json:"patchText"`
}

type PatchOption func(*PatchTool)

func WithPatchPathPolicy(policy PathPolicy) PatchOption {
	return func(t *PatchTool) { t.pathPolicy = policy }
}
func WithPatchWorkspace(ws string) PatchOption {
	return func(t *PatchTool) { t.workspace = ws }
}
func WithPatchOperations(ops operations.FileOperations) PatchOption {
	return func(t *PatchTool) { t.ops = ops }
}
func WithPatchBackupManager(bm *BackupManager) PatchOption {
	return func(t *PatchTool) { t.backupMgr = bm }
}
func WithPatchMutationQueue(q MutationQueue) PatchOption {
	return func(t *PatchTool) { t.mutationQueue = q }
}
func WithPatchReadTracker(tracker *ReadTracker) PatchOption {
	return func(t *PatchTool) { t.readTracker = tracker }
}

func NewPatchTool(opts ...PatchOption) *PatchTool {
	t := &PatchTool{}
	for _, opt := range opts {
		opt(t)
	}
	if t.ops == nil {
		t.ops = operations.LocalFileOperations{}
	}
	return t
}

func (t *PatchTool) Name() string { return "patch" }
func (t *PatchTool) Description() string {
	return "Apply a standard unified diff after verifying all file contents and hunk positions."
}
func (t *PatchTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"patchText": map[string]any{"type": "string", "description": "Complete unified diff."},
	}, "required": []string{"patchText"}}
}
func (t *PatchTool) Validate(raw json.RawMessage) (json.RawMessage, error) {
	var params PatchParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.PatchText) == "" {
		return nil, fmt.Errorf("patchText is required")
	}
	return json.Marshal(params)
}
func (t *PatchTool) RequiresConfirmation(raw json.RawMessage) (string, bool) {
	var params PatchParams
	if json.Unmarshal(raw, &params) != nil {
		return "即将应用 patch（参数解析失败）", true
	}
	hunks, err := parseUnifiedDiff(params.PatchText)
	if err != nil {
		return "即将应用 patch（补丁解析失败）", true
	}
	var paths []string
	for _, h := range hunks {
		paths = append(paths, h.Path)
	}
	return fmt.Sprintf("即将应用 patch（%d 个文件）\n  %s", len(paths), strings.Join(paths, "\n  ")), true
}
func (t *PatchTool) RequiresConfirmationAvailable() bool { return true }

type patchHunk struct {
	Path   string
	Type   string
	Chunks []diffChunk
}
type diffChunk struct {
	OldStart, OldCount int
	NewStart, NewCount int
	Lines              []diffLine
}
type diffLine struct {
	Type      byte
	Content   string
	NoNewline bool
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?:.*)$`)

func patchPath(header, prefix string) (string, error) {
	name, _, _ := strings.Cut(strings.TrimPrefix(header, prefix), "\t")
	if name == "/dev/null" {
		return name, nil
	}
	if name == "" || strings.HasPrefix(name, `"`) {
		return "", fmt.Errorf("unsupported or empty diff path")
	}
	if prefix == "--- " {
		name = strings.TrimPrefix(name, "a/")
	} else {
		name = strings.TrimPrefix(name, "b/")
	}
	if name == "" {
		return "", fmt.Errorf("empty diff path")
	}
	return name, nil
}

// parseUnifiedDiff rejects unsupported or malformed input rather than applying
// a partial parse. Hunk counts and every context/removal line are verified.
func parseUnifiedDiff(text string) ([]patchHunk, error) {
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	var files []patchHunk
	seen := map[string]bool{}
	for i := 0; i < len(lines); {
		line := lines[i]
		if line == "" || strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "new file mode ") || strings.HasPrefix(line, "deleted file mode ") {
			i++
			continue
		}
		if !strings.HasPrefix(line, "--- ") || i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
			return nil, fmt.Errorf("invalid diff header at line %d", i+1)
		}
		oldPath, err := patchPath(line, "--- ")
		if err != nil {
			return nil, err
		}
		newPath, err := patchPath(lines[i+1], "+++ ")
		if err != nil {
			return nil, err
		}
		i += 2
		h := patchHunk{Path: newPath, Type: "update"}
		switch {
		case oldPath == "/dev/null" && newPath != "/dev/null":
			h.Type = "add"
		case newPath == "/dev/null" && oldPath != "/dev/null":
			h.Path, h.Type = oldPath, "delete"
		case oldPath == "/dev/null" || oldPath != newPath:
			return nil, fmt.Errorf("renames and invalid diff paths are not supported")
		}
		key := filepath.Clean(h.Path)
		if seen[key] {
			return nil, fmt.Errorf("duplicate patch path %s", h.Path)
		}
		seen[key] = true
		for i < len(lines) && strings.HasPrefix(lines[i], "@@ ") {
			m := hunkHeader.FindStringSubmatch(lines[i])
			if m == nil {
				return nil, fmt.Errorf("invalid hunk header at line %d", i+1)
			}
			nums := [4]int{}
			for n := range nums {
				if m[n+1] == "" {
					nums[n] = 1
					continue
				}
				value, e := strconv.Atoi(m[n+1])
				if e != nil {
					return nil, fmt.Errorf("invalid hunk range")
				}
				nums[n] = value
			}
			chunk := diffChunk{OldStart: nums[0], OldCount: nums[1], NewStart: nums[2], NewCount: nums[3]}
			i++
			oldCount, newCount := 0, 0
			for oldCount < chunk.OldCount || newCount < chunk.NewCount {
				if i >= len(lines) || len(lines[i]) == 0 {
					return nil, fmt.Errorf("incomplete hunk")
				}
				line := lines[i]
				if line[0] != ' ' && line[0] != '-' && line[0] != '+' {
					return nil, fmt.Errorf("invalid hunk line %d", i+1)
				}
				if line[0] != '+' {
					oldCount++
				}
				if line[0] != '-' {
					newCount++
				}
				if oldCount > chunk.OldCount || newCount > chunk.NewCount {
					return nil, fmt.Errorf("hunk line count mismatch")
				}
				entry := diffLine{Type: line[0], Content: line[1:]}
				i++
				if i < len(lines) && lines[i] == `\ No newline at end of file` {
					entry.NoNewline = true
					i++
				}
				chunk.Lines = append(chunk.Lines, entry)
			}
			if len(chunk.Lines) == 0 {
				return nil, fmt.Errorf("empty hunk")
			}
			h.Chunks = append(h.Chunks, chunk)
		}
		if len(h.Chunks) == 0 {
			return nil, fmt.Errorf("no hunks for %s", h.Path)
		}
		files = append(files, h)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no hunks in patch")
	}
	return files, nil
}

func applyHunksToContent(original []byte, chunks []diffChunk) ([]byte, error) {
	lines := bytes.SplitAfter(original, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	var out [][]byte
	pos := 0
	for _, chunk := range chunks {
		start := chunk.OldStart
		if chunk.OldCount > 0 {
			start--
		}
		if start < pos || start > len(lines) {
			return nil, fmt.Errorf("hunk position does not match file")
		}
		out = append(out, lines[pos:start]...)
		pos = start
		newStart := chunk.NewStart
		if chunk.NewCount > 0 {
			newStart--
		}
		if newStart != len(out) {
			return nil, fmt.Errorf("new hunk position does not match file")
		}
		for _, line := range chunk.Lines {
			content := []byte(line.Content)
			if !line.NoNewline {
				content = append(content, '\n')
			}
			if line.Type != '+' {
				if pos >= len(lines) || !bytes.Equal(lines[pos], content) {
					return nil, fmt.Errorf("hunk content does not match file")
				}
				pos++
			}
			if line.Type != '-' {
				out = append(out, content)
			}
		}
	}
	out = append(out, lines[pos:]...)
	for i, line := range out {
		if i < len(out)-1 && (len(line) == 0 || line[len(line)-1] != '\n') {
			return nil, fmt.Errorf("non-final line has no newline")
		}
	}
	return bytes.Join(out, nil), nil
}

type patchChange struct {
	path, kind    string
	before, after []byte
	mode          fs.FileMode
}

func (t *PatchTool) Execute(ctx context.Context, raw json.RawMessage, _ func(agent.PartialResult)) (agent.ToolResult, error) {
	var params PatchParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return agent.ToolResult{IsError: true}, err
	}
	hunks, err := parseUnifiedDiff(params.PatchText)
	if err != nil {
		return agent.ToolResult{IsError: true, Content: err.Error()}, err
	}
	paths := make([]string, 0, len(hunks))
	seen := map[string]bool{}
	for i := range hunks {
		p := ResolvePath(t.workspace, hunks[i].Path)
		if !t.pathPolicy.Allows(t.workspace, p) || seen[p] {
			return agent.ToolResult{IsError: true}, fmt.Errorf("invalid or duplicate patch path: %s", hunks[i].Path)
		}
		seen[p] = true
		hunks[i].Path = p
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var locked func(int) (agent.ToolResult, error)
	locked = func(i int) (agent.ToolResult, error) {
		if i == len(paths) || t.mutationQueue == nil {
			return t.apply(ctx, hunks)
		}
		return t.mutationQueue.Execute(ctx, paths[i], func() (agent.ToolResult, error) { return locked(i + 1) })
	}
	return locked(0)
}

func (t *PatchTool) apply(ctx context.Context, hunks []patchHunk) (agent.ToolResult, error) {
	fail := func(err error) (agent.ToolResult, error) {
		return agent.ToolResult{IsError: true, Content: err.Error()}, err
	}
	remover, canRemove := t.ops.(operations.FileRemover)
	creator, canCreate := t.ops.(operations.FileCreator)
	changes := make([]patchChange, 0, len(hunks))
	for _, h := range hunks {
		change := patchChange{path: h.Path, kind: h.Type, mode: 0o644}
		if h.Type == "add" {
			if _, err := t.ops.Stat(ctx, h.Path); err == nil {
				return fail(fmt.Errorf("file already exists: %s", h.Path))
			} else if !isNotExist(err) {
				return fail(err)
			}
			if t.readTracker.HasRead(h.Path) {
				return fail(fmt.Errorf("file was removed after being read: %s", h.Path))
			}
			if !canRemove || !canCreate {
				return fail(fmt.Errorf("file backend cannot safely create and roll back a file"))
			}
		} else {
			info, err := t.ops.Stat(ctx, h.Path)
			if err != nil {
				return fail(err)
			}
			if info.IsDir {
				return fail(fmt.Errorf("patch path is a directory: %s", h.Path))
			}
			if info.Mode.Perm() != 0 {
				change.mode = info.Mode.Perm()
			}
			change.before, err = t.ops.ReadFile(ctx, h.Path)
			if err != nil {
				return fail(err)
			}
			if stale, _, _ := t.readTracker.CheckStale(h.Path, change.before, info.ModTime); stale {
				return fail(fmt.Errorf("file changed since last read: %s", h.Path))
			}
		}
		var err error
		change.after, err = applyHunksToContent(change.before, h.Chunks)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", h.Path, err))
		}
		if h.Type == "delete" && (len(change.after) != 0 || !canRemove) {
			return fail(fmt.Errorf("invalid or unsupported deletion: %s", h.Path))
		}
		changes = append(changes, change)
	}
	_, local := t.ops.(operations.LocalFileOperations)
	if _, ok := t.ops.(*operations.LocalFileOperations); ok {
		local = true
	}
	if t.backupMgr != nil && local {
		for _, c := range changes {
			if _, err := t.backupMgr.Snapshot(c.path); err != nil {
				return fail(fmt.Errorf("backup %s: %w", c.path, err))
			}
		}
	}
	for i, c := range changes {
		if c.kind != "add" {
			current, err := t.ops.ReadFile(ctx, c.path)
			if err != nil || !bytes.Equal(current, c.before) {
				return t.rollback(ctx, changes[:i], fmt.Errorf("file changed while applying patch: %s", c.path))
			}
		} else if _, err := t.ops.Stat(ctx, c.path); err == nil || !isNotExist(err) {
			return t.rollback(ctx, changes[:i], fmt.Errorf("new file became unavailable while applying patch: %s", c.path))
		}
		var err error
		attempted := true
		switch c.kind {
		case "delete":
			err = remover.RemoveFile(ctx, c.path)
		case "add":
			attempted = false // CreateFile cleans up its own partial output on failure.
			if err = t.ops.MkdirAll(ctx, filepath.Dir(c.path), 0o755); err == nil {
				err = creator.CreateFile(ctx, c.path, c.after, c.mode)
			}
		default:
			err = t.ops.WriteFile(ctx, c.path, c.after, c.mode)
		}
		if err != nil {
			end := i
			if attempted {
				end++
			}
			return t.rollback(ctx, changes[:end], fmt.Errorf("apply %s: %w", c.path, err))
		}
	}
	for _, c := range changes {
		if c.kind == "delete" {
			t.readTracker.Invalidate(c.path)
		} else {
			t.readTracker.Record(c.path, c.after, time.Time{})
		}
	}
	return agent.ToolResult{Content: fmt.Sprintf("Patch applied successfully to %d file(s).", len(changes))}, nil
}

func (t *PatchTool) rollback(ctx context.Context, changes []patchChange, cause error) (agent.ToolResult, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var failures []string
	for i := len(changes) - 1; i >= 0; i-- {
		c := changes[i]
		var err error
		current, readErr := t.ops.ReadFile(ctx, c.path)
		switch {
		case c.kind == "add" && readErr != nil && isNotExist(readErr):
			continue
		case c.kind == "add" && readErr == nil && bytes.Equal(current, c.after):
			err = t.ops.(operations.FileRemover).RemoveFile(ctx, c.path)
		case c.kind == "delete" && readErr != nil && isNotExist(readErr):
			err = t.ops.WriteFile(ctx, c.path, c.before, c.mode)
		case c.kind != "add" && readErr == nil && bytes.Equal(current, c.before):
			continue
		case c.kind == "update" && readErr == nil && bytes.Equal(current, c.after):
			err = t.ops.WriteFile(ctx, c.path, c.before, c.mode)
		default:
			err = fmt.Errorf("file changed during rollback; inspect it manually")
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", c.path, err))
		}
	}
	if len(failures) > 0 {
		cause = fmt.Errorf("%w; rollback failed: %s", cause, strings.Join(failures, "; "))
	}
	return agent.ToolResult{IsError: true, Content: cause.Error()}, cause
}
