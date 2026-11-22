package feishu

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// notificationOutbox retains proactive messages for retry across bridge restarts.
// Its one-hour lifetime stays inside Feishu's UUID deduplication window.
type notificationOutbox struct{ path, appID string }

func (o *notificationOutbox) open(ctx context.Context) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(o.path), 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(o.path))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.Base(o.path), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if os.IsExist(err) {
		f, err = root.OpenFile(filepath.Base(o.path), os.O_RDWR, 0)
	}
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", o.path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS notifications(id TEXT PRIMARY KEY, app_id TEXT NOT NULL, chat_id TEXT NOT NULL, body TEXT NOT NULL, created_ms INTEGER NOT NULL, next_ms INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'pending');`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (o *notificationOutbox) enqueue(ctx context.Context, id, chat, message string) error {
	db, err := o.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	now := time.Now().UnixMilli()
	_, err = db.ExecContext(ctx, `INSERT INTO notifications(id,app_id,chat_id,body,created_ms,next_ms) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, o.appID, chat, message, now, now)
	return err
}

func notificationID(appID, chat, messageID, callID string) string {
	sum := sha256.Sum256([]byte(appID + "\x00" + chat + "\x00" + messageID + "\x00" + callID))
	return hex.EncodeToString(sum[:16])
}

// StartNotificationWorker ties retries to the bridge lifecycle.
func (h *Handler) StartNotificationWorker(ctx context.Context) {
	if h.outbox == nil || h.client == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			h.deliverNotifications(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-h.outboxWake:
			}
		}
	}()
}

func (h *Handler) deliverNotifications(ctx context.Context) {
	h.outboxMu.Lock()
	defer h.outboxMu.Unlock()
	db, err := h.outbox.open(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("notification outbox unavailable", "error", err)
		}
		return
	}
	defer db.Close()
	now := time.Now().UnixMilli()
	// Never retry beyond the service's deduplication window; preserve the
	// failed entry for diagnosis instead of risking duplicate delivery.
	expired, err := db.ExecContext(ctx, `UPDATE notifications SET state='failed' WHERE app_id=? AND state='pending' AND created_ms<?`, h.appID, now-int64(50*time.Minute/time.Millisecond))
	if err != nil {
		return
	}
	if count, err := expired.RowsAffected(); err == nil && count > 0 {
		slog.Error("feishu notifications expired without delivery", "count", count)
	}
	rows, err := db.QueryContext(ctx, `SELECT id,chat_id,body,attempts FROM notifications WHERE app_id=? AND state='pending' AND next_ms<=? ORDER BY created_ms LIMIT 20`, h.appID, now)
	if err != nil {
		return
	}
	type entry struct {
		id, chat, body string
		attempts       int
	}
	var items []entry
	for rows.Next() {
		var x entry
		if err = rows.Scan(&x.id, &x.chat, &x.body, &x.attempts); err != nil {
			break
		}
		items = append(items, x)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return
	}
	for _, x := range items {
		if ctx.Err() != nil {
			return
		}
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = h.client.sendMarkdownOnce(sendCtx, x.chat, x.body, x.id)
		cancel()
		if err == nil {
			_, err = db.ExecContext(ctx, `UPDATE notifications SET state='sent',body='' WHERE id=?`, x.id)
		} else {
			delay := time.Duration(1<<min(x.attempts, 6)) * 15 * time.Second
			_, err = db.ExecContext(ctx, `UPDATE notifications SET attempts=attempts+1,next_ms=? WHERE id=?`, time.Now().Add(delay).UnixMilli(), x.id)
			slog.Warn("feishu notification delivery failed; retry scheduled", "notification_id", x.id)
		}
		if err != nil {
			slog.Warn("notification state update failed", "error", err)
			return
		}
	}
}

func (h *Handler) enqueueBroadcast(ctx context.Context, chat, incoming, callID, message string) error {
	if strings.TrimSpace(callID) == "" {
		callID = uuid.NewString()
	}
	if h.outbox == nil {
		return fmt.Errorf("notification outbox unavailable")
	}
	id := notificationID(h.appID, chat, incoming, callID)
	return h.outbox.enqueue(ctx, id, chat, message)
}
