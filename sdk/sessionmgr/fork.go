package sessionmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hwj123hwj/easyagent/sdk/session"
)

// ErrForkPointUnavailable means the selected snapshot cannot supply a complete
// history prefix at the requested entry. It does not describe storage failures.
var ErrForkPointUnavailable = errors.New("fork point unavailable")

// ForkResult describes a durable fork. Counts exclude synthetic compaction
// summaries and are derived from the same source snapshot used to create it.
type ForkResult struct {
	ID               string
	Path             string
	KeptMessages     int
	KeptUserMessages int
	DroppedMessages  int
}

// ForkSnapshotWithStats selects a complete history prefix, persists it, and
// returns its conversation counts without reopening either session file.
func (m *Manager) ForkSnapshotWithStats(ctx context.Context, sourceID string, entries []session.Entry, entryID *string, application *string) (result ForkResult, err error) {
	if sourceID == "" || strings.ContainsAny(sourceID, `/\`) || sourceID == "." || sourceID == ".." {
		return ForkResult{}, fmt.Errorf("invalid source session")
	}
	sourceCount := len(session.HistoryFromEntries(entries))
	entries, err = forkPrefix(entries, entryID)
	if err != nil {
		return ForkResult{}, fmt.Errorf("%w: %v", ErrForkPointUnavailable, err)
	}
	// Cut raw entries before projecting history so a later micro-compaction
	// marker cannot affect a fork at an earlier message.
	history := session.HistoryFromEntries(entries)
	result.KeptMessages = len(history)
	result.DroppedMessages = max(0, sourceCount-len(history))
	for _, entry := range history {
		if entry.User != nil {
			result.KeptUserMessages++
		}
	}
	var data []byte
	for _, entry := range entries {
		encoded, encodeErr := json.Marshal(entry)
		if encodeErr != nil {
			return ForkResult{}, encodeErr
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	newID, newPath, err := m.Create(ctx)
	if err != nil {
		return ForkResult{}, err
	}
	defer func() {
		if err != nil {
			_ = m.Delete(newID)
		}
	}()
	if err = os.WriteFile(newPath, data, 0o600); err != nil {
		return ForkResult{}, err
	}
	if err = m.copyMeta(sourceID, newID, application); err != nil {
		return ForkResult{}, err
	}
	if err = m.copyAttachments(sourceID, newID); err != nil {
		return ForkResult{}, err
	}
	result.ID, result.Path = newID, newPath
	return result, nil
}
