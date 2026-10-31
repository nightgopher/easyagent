package agentdata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// PublishMemory accepts an explicitly reviewed Markdown draft. It is an owner
// API operation, not a tool exposed to the model. Canonical knowledge stays on disk.
func (s *Store) PublishMemory(ctx context.Context, agentID, memoryID, sourceID, directory, title, content string, version int64) (string, error) {
	m, err := s.GetMemory(ctx, agentID, memoryID)
	if err != nil {
		return "", err
	}
	if m.Version != version {
		return "", ErrConflict
	}
	source, err := s.AgentSource(ctx, agentID, sourceID, true)
	if err != nil {
		return "", err
	}
	if !slices.Contains(source.Directories, directory) {
		return "", fmt.Errorf("choose one of the source directories")
	}
	title, content = strings.TrimSpace(title), strings.TrimSpace(content)
	if title == "" || len([]rune(title)) > 120 || strings.ContainsAny(title, "\r\n\x00") || content == "" || len(content) > 64*1024 || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return "", fmt.Errorf("a title and reviewed Markdown content (up to 64 KiB) are required")
	}
	name := path.Join(directory, fmt.Sprintf("%s-v%d.md", m.ID, version))
	if !documentAllowed(source, name) {
		return "", fmt.Errorf("destination is outside the selected knowledge directories")
	}
	root, err := os.OpenRoot(source.Path)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	parts := strings.Split(directory, "/")
	for i := range parts {
		info, err := root.Lstat(path.Join(parts[:i+1]...))
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("destination must be an existing directory without symlinks")
		}
	}
	body := fmt.Sprintf("# %s\n\n%s\n\n---\n来源：Agent `%s` · 记忆 `%s` · 版本 %d（人工整理）\n", title, content, agentID, memoryID, version)
	if old, err := readDocument(root, name); err == nil {
		if old == body {
			return name, nil
		}
		return "", ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	temp := path.Join(directory, ".publish-"+uuid.NewString())
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Remove(temp) }()
	_, err = f.WriteString(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = root.Link(temp, name); err != nil {
		if errors.Is(err, os.ErrExist) {
			if old, readErr := readDocument(root, name); readErr == nil && old == body {
				return name, nil
			}
			return "", ErrConflict
		}
		return "", err
	}
	return name, nil
}
