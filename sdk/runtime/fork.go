package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/hwj123hwj/easyagent/sdk/agent"
	"github.com/hwj123hwj/easyagent/sdk/session"
	"github.com/hwj123hwj/easyagent/sdk/sessionmgr"
)

// ForkOptions selects one fork point. Nil selectors copy the whole branch;
// an empty EntryID clears it. BeforeMessageIndex counts user messages from zero.
// Application nil inherits the source; an empty string selects the default agent.
type ForkOptions struct {
	EntryID            *string
	BeforeMessageIndex *int
	Application        *string
}

// Fork snapshots this session while blocking concurrent prompts and mutations.
func (s *AgentSession) Fork(ctx context.Context, entryID *string) (string, string, error) {
	return s.ForkTo(ctx, entryID, nil)
}

// ForkTo permits immutable history prefixes during a run, never its mutable tail.
func (s *AgentSession) ForkTo(ctx context.Context, entryID *string, application *string) (string, string, error) {
	result, err := s.ForkWithOptions(ctx, ForkOptions{EntryID: entryID, Application: application})
	return result.ID, result.Path, err
}

// ForkWithOptions resolves the fork point, writes the fork, and counts its
// history under one runtime lock. During a run all operations use runPrefix,
// excluding live messages and compactions added after that snapshot was taken.
func (s *AgentSession) ForkWithOptions(ctx context.Context, options ForkOptions) (sessionmgr.ForkResult, error) {
	if options.EntryID != nil && options.BeforeMessageIndex != nil {
		return sessionmgr.ForkResult{}, fmt.Errorf("select one fork point")
	}
	if options.BeforeMessageIndex != nil && *options.BeforeMessageIndex < 0 {
		return sessionmgr.ForkResult{}, fmt.Errorf("before_message_index must be >= 0")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mutating {
		return sessionmgr.ForkResult{}, agent.ErrAgentBusy
	}
	busy := s.busyLocked()
	entries := s.runPrefix
	if busy {
		if options.EntryID == nil && options.BeforeMessageIndex == nil {
			return sessionmgr.ForkResult{}, agent.ErrAgentBusy
		}
	} else {
		var err error
		entries, err = s.session.Storage().GetPathToRoot(ctx, "")
		if err != nil {
			return sessionmgr.ForkResult{}, err
		}
	}
	target := options.EntryID
	if options.BeforeMessageIndex != nil {
		index, seen := *options.BeforeMessageIndex, 0
		found := false
		history := session.HistoryFromEntries(entries)
		for i, entry := range history {
			if entry.User == nil {
				continue
			}
			if seen == index {
				value := ""
				if index > 0 {
					value = history[i-1].ID
				}
				target, found = &value, true
				break
			}
			seen++
		}
		if !found {
			if busy {
				return sessionmgr.ForkResult{}, agent.ErrAgentBusy
			}
			return sessionmgr.ForkResult{}, fmt.Errorf("user message index not found")
		}
	}
	result, err := s.sessionMgr.ForkSnapshotWithStats(ctx, s.sessionID, entries, target, options.Application)
	if busy && errors.Is(err, sessionmgr.ErrForkPointUnavailable) {
		return sessionmgr.ForkResult{}, agent.ErrAgentBusy
	}
	return result, err
}
