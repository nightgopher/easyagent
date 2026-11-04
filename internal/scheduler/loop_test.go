package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestLoopConcurrentReplacementStopsEveryTrigger(t *testing.T) {
	mgr := NewLoopManager()
	defer mgr.StopAll()
	var starters sync.WaitGroup
	entries := make(chan *loopEntry, 20)
	for range 20 {
		starters.Add(1)
		go func() {
			defer starters.Done()
			_, err := mgr.StartNoMinInterval("same-session", "check", time.Millisecond, time.Hour, func(ctx context.Context, _ string) error {
				<-ctx.Done()
				return ctx.Err()
			})
			if err != nil {
				t.Error(err)
				return
			}
			mgr.mu.Lock()
			entries <- mgr.loops["same-session"]
			mgr.mu.Unlock()
		}()
	}
	starters.Wait()
	close(entries)
	mgr.StopAll()
	for entry := range entries {
		select {
		case <-entry.done:
		case <-time.After(time.Second):
			t.Fatal("replaced loop survived StopAll")
		}
	}
	if mgr.Get("same-session") != nil {
		t.Fatal("expected loop entry to be removed after StopAll")
	}
}
