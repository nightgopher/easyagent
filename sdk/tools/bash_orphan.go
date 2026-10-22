package tools

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const backgroundProcessTokenEnv = "EASYAGENT_BACKGROUND_TASK_ID"

func validBackgroundToken(token string) bool {
	if len(token) != 32 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

// The inherited token identifies the whole process group even if its leader has
// exited. Command names and reused PIDs alone are never sufficient to kill it.
type orphanRecord struct {
	PGID      int    `json:"pgid"`
	OwnerPID  int    `json:"owner_pid"`
	Token     string `json:"token"`
	StartedAt int64  `json:"started_at"`
}

type orphanLedger struct{ path string }

func (l *orphanLedger) markerPath(token string) string {
	return filepath.Join(filepath.Dir(l.path), ".background-task-"+token)
}

func (l *orphanLedger) createMarker(token string) (*os.File, error) {
	if l == nil {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(l.markerPath(token), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
}

func newOrphanLedger(dataDir string) *orphanLedger {
	if dataDir == "" || !backgroundTrackingSupported {
		return nil
	}
	return &orphanLedger{path: filepath.Join(dataDir, "background-tasks.json")}
}

// Lock the ledger across App instances and processes sharing a data directory.
// A rename alone cannot protect a read-modify-write operation.
func (l *orphanLedger) update(fn func([]orphanRecord) []orphanRecord) error {
	if l == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(l.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockOrphanLedger(lock); err != nil {
		return err
	}
	defer unlockOrphanLedger(lock)
	var records []orphanRecord
	data, err := os.ReadFile(l.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &records); err != nil {
			return fmt.Errorf("invalid background task ledger: %w", err)
		}
	}
	records = fn(records)
	data, err = json.Marshal(records)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".background-tasks-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), l.path)
}

func (l *orphanLedger) add(rec orphanRecord) error {
	return l.update(func(records []orphanRecord) []orphanRecord { return append(records, rec) })
}

func (l *orphanLedger) remove(pgid int, token string) {
	if pgid <= 1 {
		return
	}
	err := l.update(func(records []orphanRecord) []orphanRecord {
		kept := records[:0]
		for _, rec := range records {
			if rec.PGID != pgid || rec.Token != token {
				kept = append(kept, rec)
			}
		}
		return kept
	})
	if err != nil {
		slog.Warn("remove background task record", "pgid", pgid, "error", err)
	} else if l != nil {
		_ = os.Remove(l.markerPath(token))
	}
}

func (l *orphanLedger) cleanup() error {
	return l.update(func(records []orphanRecord) []orphanRecord {
		kept := records[:0]
		for _, rec := range records {
			if rec.PGID <= 1 || rec.OwnerPID <= 1 || !validBackgroundToken(rec.Token) {
				continue
			}
			if processAlive(rec.OwnerPID) {
				kept = append(kept, rec)
				continue
			}
			if killOrphanProcessGroup(rec) {
				slog.Info("cleaned up orphaned background task", "pgid", rec.PGID,
					"age", time.Since(time.Unix(rec.StartedAt, 0)).Round(time.Second))
			} else if processGroupAlive(rec.PGID) {
				// Keep evidence of an unverifiable group; never guess its identity.
				kept = append(kept, rec)
				continue
			}
			_ = os.Remove(l.markerPath(rec.Token))
		}
		return kept
	})
}

func newBackgroundProcessToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// CleanupBackgroundTasks reclaims recorded local groups whose owning process
// exited. Call once at application startup, before accepting work.
func CleanupBackgroundTasks(dataDir string) {
	if err := newOrphanLedger(dataDir).cleanup(); err != nil {
		slog.Warn("recover background tasks", "error", err)
	}
}
