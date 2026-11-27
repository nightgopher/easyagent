//go:build windows

package codex

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func openCredential(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("invalid credential file")
	}
	return os.Open(path) // #nosec G304 -- fixed credential file in the user's private account directory
}
func lockCredentials(ctx context.Context, path string) (func(), error) {
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("invalid lock file")
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- fixed lock file in the user's private account directory
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	var o windows.Overlapped
	for {
		err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &o)
		if err == nil {
			return func() { _ = windows.UnlockFileEx(h, 0, 1, 0, &o); _ = f.Close() }, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = f.Close()
			return nil, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
