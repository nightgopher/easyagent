// Package scheduler provides recurring task scheduling for agent sessions.
//
// Inspired by hwjcode's /loop watchdog command, implemented in Go with
// goroutine + time.Ticker for lightweight, session-scoped recurring prompts.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// LoopContext holds the state of an active loop.
type LoopContext struct {
	Prompt    string        `json:"prompt"`
	Interval  time.Duration `json:"interval"`
	ExpiresAt time.Time     `json:"expires_at"`
	StartedAt time.Time     `json:"started_at"`
	LastRunAt time.Time     `json:"last_run_at"`
	Pending   bool          `json:"pending"`
}

// IsActive returns true if the loop is still within its expiry window.
func (lc *LoopContext) IsActive() bool {
	return time.Now().Before(lc.ExpiresAt)
}

// Remaining returns the time until the loop expires.
func (lc *LoopContext) Remaining() time.Duration {
	return time.Until(lc.ExpiresAt)
}

// TriggerFunc is called when the loop fires. The implementation is responsible
// for injecting the prompt into the agent and collecting results.
type TriggerFunc func(ctx context.Context, prompt string) error

// ErrNoTrigger prevents entrypoints without prompt dispatch from accepting loops.
var ErrNoTrigger = errors.New("/loop is not supported in this interface; use easyagent chat without --legacy, or serve")

// TriggerResolver resolves a sessionID to a TriggerFunc.
// The entrypoint sets this so loops can inject prompts into the right session.
type TriggerResolver func(sessionID string) TriggerFunc

type loopEntry struct {
	context LoopContext
	ticker  *time.Ticker
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
}

// LoopManager manages per-session recurring loops.
// Each session can have at most one active loop (matching hwjcode semantics).
type LoopManager struct {
	mu       sync.Mutex
	loops    map[string]*loopEntry // sessionID -> active loop
	resolver TriggerResolver       // resolves sessionID → trigger func
}

// NewLoopManager creates a new LoopManager.
func NewLoopManager() *LoopManager {
	return &LoopManager{
		loops: make(map[string]*loopEntry),
	}
}

// SetTriggerResolver sets the function used to resolve sessionID → TriggerFunc.
// Called by the entrypoint to wire prompt injection.
func (m *LoopManager) SetTriggerResolver(r TriggerResolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolver = r
}

// Start begins a recurring loop for the given session.
// If a loop already exists for this session, it is replaced.
//
// Parameters:
//   - sessionID: unique session identifier
//   - prompt: the prompt text to inject on each tick
//   - interval: time between ticks (minimum 1 minute in production)
//   - ttl: maximum lifetime of the loop (0 = 3 days default)
//   - trigger: callback invoked on each tick
//
// Returns the LoopContext for the newly started loop.
func (m *LoopManager) Start(sessionID, prompt string, interval, ttl time.Duration, trigger TriggerFunc) (*LoopContext, error) {
	return m.startInternal(sessionID, prompt, interval, ttl, trigger, false)
}

// StartNoMinInterval is like Start but bypasses the minimum interval check.
// Used only by tests.
func (m *LoopManager) StartNoMinInterval(sessionID, prompt string, interval, ttl time.Duration, trigger TriggerFunc) (*LoopContext, error) {
	return m.startInternal(sessionID, prompt, interval, ttl, trigger, true)
}

// startInternal starts a loop. If skipMinInterval is true, bypasses the 1-minute minimum.
// The trigger stored in the loop is resolved at tick-time via the resolver if set.
func (m *LoopManager) startInternal(sessionID, prompt string, interval, ttl time.Duration, trigger TriggerFunc, skipMinInterval bool) (*LoopContext, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("sessionID is required")
	}
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if !skipMinInterval && interval < time.Minute {
		return nil, fmt.Errorf("interval must be at least 1 minute")
	}
	if ttl <= 0 {
		ttl = 3 * 24 * time.Hour // default: 3 days
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if trigger == nil && m.resolver == nil {
		return nil, ErrNoTrigger
	}
	if interval <= 0 {
		return nil, fmt.Errorf("interval must be positive")
	}
	// Replace atomically so concurrent starts cannot leave an untracked loop.
	if existing, ok := m.loops[sessionID]; ok {
		m.stopLocked(sessionID, existing)
	}

	now := time.Now()
	lc := LoopContext{
		Prompt:    prompt,
		Interval:  interval,
		ExpiresAt: now.Add(ttl),
		StartedAt: now,
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry := &loopEntry{
		context: lc,
		ticker:  time.NewTicker(interval),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}

	m.loops[sessionID] = entry

	slog.Info("loop started",
		"session", sessionID,
		"interval", interval.String(),
		"expires", lc.ExpiresAt.Format(time.RFC3339),
		"prompt", prompt,
	)

	go m.runLoop(sessionID, entry, trigger)

	return &lc, nil
}

// runLoop is the goroutine that fires the trigger on each tick.
func (m *LoopManager) runLoop(sessionID string, entry *loopEntry, trigger TriggerFunc) {
	defer close(entry.done)
	defer entry.ticker.Stop()

	for {
		select {
		case <-entry.ctx.Done():
			return

		case <-entry.ticker.C:
			if entry.ctx.Err() != nil {
				return
			}
			// Check expiry
			if !entry.context.IsActive() {
				slog.Info("loop expired, auto-stopping", "session", sessionID)
				m.mu.Lock()
				if m.loops[sessionID] == entry {
					m.stopLocked(sessionID, entry)
				}
				m.mu.Unlock()
				return
			}

			// Update lastRunAt
			m.mu.Lock()
			entry.context.LastRunAt = time.Now()
			entry.context.Pending = true
			m.mu.Unlock()

			// Resolve the actual trigger: prefer resolver (wired by mode layer),
			// fallback to the trigger passed at Start time.
			effectiveTrigger := trigger
			m.mu.Lock()
			resolver := m.resolver
			m.mu.Unlock()
			if resolver != nil {
				if resolved := resolver(sessionID); resolved != nil {
					effectiveTrigger = resolved
				}
			}

			// Fire trigger with a generous context (5 minutes per run)
			ctx, cancel := context.WithTimeout(entry.ctx, 5*time.Minute)
			slog.Info("loop firing", "session", sessionID, "prompt", entry.context.Prompt)

			var err error
			if ctx.Err() != nil {
				err = ctx.Err()
			} else if effectiveTrigger == nil {
				err = ErrNoTrigger
			} else {
				err = effectiveTrigger(ctx, entry.context.Prompt)
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("loop trigger failed", "session", sessionID, "error", err)
			}
			cancel()

			// Mark as not pending
			m.mu.Lock()
			entry.context.Pending = false
			m.mu.Unlock()
		}
	}
}

// Stop cancels the loop for the given session. No-op if none exists.
func (m *LoopManager) Stop(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if entry, ok := m.loops[sessionID]; ok {
		m.stopLocked(sessionID, entry)
	}
}

// stopLocked stops a loop entry. Caller must hold m.mu.
func (m *LoopManager) stopLocked(sessionID string, entry *loopEntry) {
	entry.cancel()
	delete(m.loops, sessionID)
	slog.Info("loop stopped", "session", sessionID)
}

// Get returns the LoopContext for the given session, or nil if none.
func (m *LoopManager) Get(sessionID string) *LoopContext {
	m.mu.Lock()
	defer m.mu.Unlock()

	if entry, ok := m.loops[sessionID]; ok {
		lc := entry.context
		return &lc
	}
	return nil
}

// StopAll stops all active loops. Called on app shutdown.
func (m *LoopManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for sessionID, entry := range m.loops {
		m.stopLocked(sessionID, entry)
	}
}
