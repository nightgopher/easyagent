package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nightgopher/easyagent/internal/scheduler"
)

type loopPromptMsg struct {
	ctx       context.Context
	sessionID string
	prompt    string
	result    chan<- error
}

// bindLoops sends scheduler events through Update, the only owner of UI state.
// The trigger waits for the turn to finish, keeping Pending and timeouts accurate.
func (m *TuiModel) bindLoops(manager *scheduler.LoopManager) func() {
	program := m.program
	manager.SetTriggerResolver(func(sessionID string) scheduler.TriggerFunc {
		return func(ctx context.Context, prompt string) error {
			result := make(chan error, 1)
			program.Send(loopPromptMsg{ctx: ctx, sessionID: sessionID, prompt: prompt, result: result})
			select {
			case err := <-result:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	return func() {
		manager.StopAll()
		manager.SetTriggerResolver(nil)
	}
}

func (m *TuiModel) receiveLoopPrompt(msg loopPromptMsg) (tea.Model, tea.Cmd) {
	if err := msg.ctx.Err(); err != nil {
		msg.result <- err
		return m, nil
	}
	var reason string
	switch {
	case m.quitting:
		reason = "terminal is closing"
	case m.session == nil || m.session.SessionID() != msg.sessionID:
		reason = "open its session to resume"
	case m.agentBusy || m.slashCmdBusy || m.session.IsBusy():
		reason = "session is busy"
	case m.modelSelect || m.completion.IsActive() || m.confirmation.IsActive() || m.questions.IsActive():
		reason = "finish the current dialog first"
	}
	if reason != "" {
		m.copyNotice = fmt.Sprintf("Loop skipped (%s): %s", msg.sessionID, reason)
		msg.result <- fmt.Errorf("%s", m.copyNotice)
		return m, nil
	}
	return m.startPrompt(msg.ctx, msg.prompt, msg.result)
}
