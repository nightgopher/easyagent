package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoopRequiresTrigger(t *testing.T) {
	mgr := NewLoopManager()
	defer mgr.StopAll()
	_, err := mgr.Start("session", "run tests", time.Minute, time.Hour, nil)
	require.Error(t, err, "an unwired entrypoint must not report a scheduled loop")
	require.Nil(t, mgr.Get("session"))
}

func TestLoopResolverCanReadStatusAndStopCancelsTrigger(t *testing.T) {
	mgr := NewLoopManager()
	defer mgr.StopAll()
	started := make(chan context.Context, 1)
	finished := make(chan struct{})
	mgr.SetTriggerResolver(func(id string) TriggerFunc {
		// Resolving an entry must not hold the manager lock.
		if mgr.Get(id) == nil {
			return nil
		}
		return func(ctx context.Context, prompt string) error {
			started <- ctx
			<-ctx.Done()
			close(finished)
			return ctx.Err()
		}
	})
	_, err := mgr.StartNoMinInterval("session", "run tests", time.Millisecond, time.Hour, nil)
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("resolver blocked before dispatch")
	}
	mgr.Stop("session")
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("stopped loop did not cancel its trigger")
	}
	require.Nil(t, mgr.Get("session"))
}

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
	require.Nil(t, mgr.Get("same-session"))
}

func TestLoopStartStop(t *testing.T) {
	mgr := NewLoopManager()
	defer mgr.StopAll()

	var fireCount int32
	trigger := func(ctx context.Context, prompt string) error {
		atomic.AddInt32(&fireCount, 1)
		return nil
	}

	// Start a loop with 100ms interval (bypassing the 1 minute minimum for tests)
	sessionID := "test-session"
	lc, err := mgr.StartNoMinInterval(sessionID, "check status", 100*time.Millisecond, 1*time.Second, trigger)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if lc.Prompt != "check status" {
		t.Errorf("Prompt = %q, want %q", lc.Prompt, "check status")
	}

	// Wait for at least 2 ticks
	time.Sleep(350 * time.Millisecond)

	mgr.Stop(sessionID)

	count := atomic.LoadInt32(&fireCount)
	if count < 2 {
		t.Errorf("Expected at least 2 fires, got %d", count)
	}

	// Verify the loop was removed
	if mgr.Get(sessionID) != nil {
		t.Error("Expected loop to be removed after Stop")
	}
}

func TestLoopReplacement(t *testing.T) {
	mgr := NewLoopManager()
	defer mgr.StopAll()

	var fireCount int32
	trigger := func(ctx context.Context, prompt string) error {
		atomic.AddInt32(&fireCount, 1)
		return nil
	}

	// Start first loop
	_, err := mgr.StartNoMinInterval("sess", "prompt1", 100*time.Millisecond, 5*time.Second, trigger)
	if err != nil {
		t.Fatalf("First Start failed: %v", err)
	}

	// Start second loop (should replace the first)
	lc, err := mgr.StartNoMinInterval("sess", "prompt2", 100*time.Millisecond, 5*time.Second, trigger)
	if err != nil {
		t.Fatalf("Second Start failed: %v", err)
	}

	if lc.Prompt != "prompt2" {
		t.Errorf("Expected prompt2, got %q", lc.Prompt)
	}

	// Only one loop should exist
	if len(mgr.loops) != 1 {
		t.Errorf("Expected 1 loop, got %d", len(mgr.loops))
	}
}

func TestLoopExpiry(t *testing.T) {
	mgr := NewLoopManager()
	defer mgr.StopAll()

	var fireCount int32
	trigger := func(ctx context.Context, prompt string) error {
		atomic.AddInt32(&fireCount, 1)
		return nil
	}

	// Start a loop with a short TTL
	_, err := mgr.StartNoMinInterval("expiry-test", "test", 100*time.Millisecond, 250*time.Millisecond, trigger)
	if err != TTLTolerance(err) {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for the loop to expire and auto-stop
	time.Sleep(400 * time.Millisecond)

	if mgr.Get("expiry-test") != nil {
		t.Error("Expected loop to be auto-removed after expiry")
	}
}

func TestLoopTriggerError(t *testing.T) {
	mgr := NewLoopManager()
	defer mgr.StopAll()

	var fireCount int32
	trigger := func(ctx context.Context, prompt string) error {
		atomic.AddInt32(&fireCount, 1)
		return errFail
	}

	_, err := mgr.StartNoMinInterval("err-test", "test", 100*time.Millisecond, 1*time.Second, trigger)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(250 * time.Millisecond)

	// Loop should still be running even though trigger returned error
	if mgr.Get("err-test") == nil {
		t.Error("Loop should continue even after trigger error")
	}
}

func TestLoopMinInterval(t *testing.T) {
	mgr := NewLoopManager()
	defer mgr.StopAll()

	_, err := mgr.Start("test", "test", 30*time.Second, 1*time.Hour, func(ctx context.Context, prompt string) error {
		return nil
	})
	if err == nil {
		t.Error("Expected error for interval < 1 minute")
	}
}

func TestLoopStatus(t *testing.T) {
	mgr := NewLoopManager()
	defer mgr.StopAll()

	// No loop: Get should return nil
	if mgr.Get("none") != nil {
		t.Error("Expected nil for non-existent loop")
	}

	// Start a loop
	lc, err := mgr.StartNoMinInterval("status-test", "run tests", 100*time.Millisecond, 2*time.Second, func(ctx context.Context, prompt string) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	got := mgr.Get("status-test")
	if got == nil {
		t.Fatal("Expected non-nil loop context")
	}
	if got.Prompt != "run tests" {
		t.Errorf("Prompt = %q, want %q", got.Prompt, "run tests")
	}
	if got.Interval != lc.Interval {
		t.Errorf("Interval = %v, want %v", got.Interval, lc.Interval)
	}
}

// ── Test helpers ──

var errFail = &simpleErr{"simulated failure"}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

// TTLTolerance returns err if non-nil (used for clarity in test).
func TTLTolerance(err error) error {
	return err
}
