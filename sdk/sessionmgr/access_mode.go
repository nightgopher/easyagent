package sessionmgr

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nightgopher/easyagent/sdk/config"
)

func (m *Manager) SaveAccessMode(id, mode string) error {
	if !config.ValidAccessMode(mode) {
		return fmt.Errorf("access mode must be ask or full")
	}
	return m.updateMeta(id, map[string]any{"access_mode": mode})
}

// LoadAccessMode returns empty for legacy sessions, but rejects corrupt metadata.
func (m *Manager) LoadAccessMode(id string) (string, error) {
	m.metaMu.Lock()
	defer m.metaMu.Unlock()
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("invalid session id")
	}
	meta, err := readMetadata(filepath.Join(m.SessionsDir(), id))
	if err != nil {
		return "", err
	}
	var mode string
	if err := meta.decodeField("access_mode", &mode); err != nil {
		return "", err
	}
	if mode != "" && !config.ValidAccessMode(mode) {
		return "", fmt.Errorf("invalid saved access mode")
	}
	return mode, nil
}
