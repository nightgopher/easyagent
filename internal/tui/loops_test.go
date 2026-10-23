package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hwj123hwj/easyagent/internal/agents/coding/commands"
	"github.com/hwj123hwj/easyagent/internal/scheduler"
	"github.com/hwj123hwj/easyagent/sdk/agent"
	"github.com/hwj123hwj/easyagent/sdk/ai"
	"github.com/hwj123hwj/easyagent/sdk/ai/providers"
	"github.com/hwj123hwj/easyagent/sdk/config"
	"github.com/hwj123hwj/easyagent/sdk/extensions"
	"github.com/hwj123hwj/easyagent/sdk/runtime"
	"github.com/hwj123hwj/easyagent/sdk/sessionmgr"
	"github.com/hwj123hwj/easyagent/sdk/slashcmd"
	"github.com/stretchr/testify/require"
)

type loopTestApplication struct{ tools []agent.Tool }

func (a loopTestApplication) BuildTools(runtime.ToolBuildOptions) []agent.Tool { return a.tools }
func (loopTestApplication) BuildPrompt(runtime.PromptBuildOptions, string, string) string {
	return "Test scheduled prompts."
}
func (loopTestApplication) NewSessionExt() runtime.SessionExt { return nil }

func newLoopModel(t *testing.T, handler http.HandlerFunc, tools ...agent.Tool) *TuiModel {
	t.Helper()
	provider := httptest.NewServer(handler)
	t.Cleanup(provider.Close)
	registry := providers.NewRegistry()
	registry.Register(providers.NewOpenAIProvider("test-key", provider.URL))
	cfg := config.Default()
	cfg.Provider, cfg.OpenAIModel = "openai", "test-model"
	cfg.DataDir, cfg.Workspace = t.TempDir(), t.TempDir()
	cfg.SkillMarketEnabled = false
	sess, err := runtime.NewAgentSession(context.Background(), runtime.AgentSessionOptions{
		Config: cfg, SkillDirs: []string{t.TempDir()},
	}, runtime.Dependencies{
		Registry: registry, SessionMgr: sessionmgr.NewManager(cfg.DataDir),
		ExtRegistry: extensions.NewRegistry(), Application: loopTestApplication{tools},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	return New(sess, slashcmd.NewRegistry(), false)
}

func startLoopProgram(t *testing.T, m *TuiModel, mgr *scheduler.LoopManager) (*tea.Program, <-chan error, <-chan tea.Msg) {
	t.Helper()
	events := make(chan tea.Msg, 64)
	p := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler(),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			// Each integration case observes one scheduled turn. Stop inside the
			// event loop before Update releases the scheduler's pending callback;
			// stopping from the test goroutine can race with the next 20ms tick.
			if event, ok := msg.(streamEventMsg); ok {
				switch event.msg.(type) {
				case StreamDoneMsg, AgentErrorMsg:
					mgr.StopAll()
				}
			}
			switch msg.(type) {
			case streamEventMsg, ConfirmationDialogMsg:
				events <- msg
			}
			return msg
		}))
	m.SetProgram(p)
	unbind := m.bindLoops(mgr)
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err; close(done) }()
	t.Cleanup(func() {
		unbind()
		p.Kill()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, tea.ErrProgramKilled) {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("TUI did not stop")
		}
	})
	p.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	return p, done, events
}

func waitLoopEvent(t *testing.T, events <-chan tea.Msg, match func(tea.Msg) bool) {
	t.Helper()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case msg := <-events:
			if stream, ok := msg.(streamEventMsg); ok {
				msg = stream.msg
			}
			if match(msg) {
				return
			}
		case <-timeout.C:
			t.Fatal("scheduled turn did not deliver the expected TUI event")
		}
	}
}

func loopAnswer(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"loop reply\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
}

func TestLoopDispatchesThroughTUIAndPreservesDraft(t *testing.T) {
	requests := make(chan map[string]any, 8)
	release := make(chan struct{})
	m := newLoopModel(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests <- body
		select {
		case <-release:
			loopAnswer(w)
		case <-r.Context().Done():
		}
	})
	m.input.insertString("尚未发送\n保留草稿")
	m.input.AddHistory("manual prompt")
	mgr := scheduler.NewLoopManager()
	commands.RegisterLoopCommands(m.slashCmds, mgr)
	p, done, events := startLoopProgram(t, m, mgr)
	// Exercise the real command, then accelerate only its timer for the test.
	result, err := m.slashCmds.Execute(slashcmd.Context{Ctx: context.Background(), Session: m.session}, "/loop 1m check status")
	require.NoError(t, err)
	require.Contains(t, result.Output, "Loop scheduled")
	lc := mgr.Get(m.session.SessionID())
	_, err = mgr.StartNoMinInterval(m.session.SessionID(), lc.Prompt, 20*time.Millisecond, time.Hour, nil)
	require.NoError(t, err)
	select {
	case body := <-requests:
		messages := body["messages"].([]any)
		require.Equal(t, "check status", messages[len(messages)-1].(map[string]any)["content"])
	case <-time.After(3 * time.Second):
		t.Fatal("loop tick never reached the mock provider")
	}
	require.True(t, mgr.Get(m.session.SessionID()).Pending)
	close(release)
	waitLoopEvent(t, events, func(msg tea.Msg) bool { _, ok := msg.(StreamDoneMsg); return ok })
	mgr.StopAll()
	p.Send(tea.Quit())
	require.NoError(t, <-done)
	require.Equal(t, "尚未发送\n保留草稿", m.input.Text())
	require.Equal(t, []string{"manual prompt"}, m.input.history)
	require.False(t, m.agentBusy)
	require.True(t, m.session.ConfirmEnabled())
	var transcript strings.Builder
	for _, message := range m.messages {
		transcript.WriteString(message.Content)
	}
	require.Contains(t, transcript.String(), "loop reply")
	stored, err := m.session.Session().BuildContext(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(stored), 2)
	require.Equal(t, "loop reply", stored[len(stored)-1].(ai.AssistantMessage).Text)
}

func TestLoopStopCancelsInFlightTUITurn(t *testing.T) {
	started, cancelled := make(chan struct{}, 1), make(chan struct{}, 1)
	m := newLoopModel(t, func(w http.ResponseWriter, r *http.Request) {
		// Consume the request so the HTTP server can observe client cancellation.
		_, _ = io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		<-r.Context().Done()
		cancelled <- struct{}{}
	})
	mgr := scheduler.NewLoopManager()
	p, done, events := startLoopProgram(t, m, mgr)
	_, err := mgr.StartNoMinInterval(m.session.SessionID(), "wait", time.Millisecond, time.Hour, nil)
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("mock request did not start")
	}
	mgr.Stop(m.session.SessionID())
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("stop left a model request running")
	}
	waitLoopEvent(t, events, func(msg tea.Msg) bool {
		switch msg.(type) {
		case AgentErrorMsg, StreamDoneMsg:
			return true
		}
		return false
	})
	p.Send(tea.Quit())
	require.NoError(t, <-done)
	require.False(t, m.agentBusy)
	require.Nil(t, mgr.Get(m.session.SessionID()))
}

func TestLoopRejectsBusyInactiveAndCancelledDispatch(t *testing.T) {
	for _, name := range []string{"busy", "command", "inactive", "dialog", "closing", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			m := newLoopModel(t, func(http.ResponseWriter, *http.Request) { t.Error("rejected loop called provider") })
			m.input.insertString("draft")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id := m.session.SessionID()
			switch name {
			case "busy":
				m.agentBusy = true
			case "command":
				m.slashCmdBusy = true
			case "inactive":
				id = "previous-session"
			case "dialog":
				m.modelSelect = true
			case "closing":
				m.quitting = true
			case "cancelled":
				cancel()
			}
			result := make(chan error, 1)
			_, cmd := m.Update(loopPromptMsg{ctx: ctx, sessionID: id, prompt: "must not run", result: result})
			require.Nil(t, cmd)
			require.Error(t, <-result)
			require.Empty(t, m.messages, "a tick must not enter another session's transcript")
			require.Equal(t, "draft", m.input.Text())
			if name != "cancelled" {
				require.Contains(t, m.copyNotice, "Loop skipped")
			}
		})
	}
}

type loopApprovalTool struct{ calls atomic.Int32 }

func (*loopApprovalTool) Name() string        { return "guarded_action" }
func (*loopApprovalTool) Description() string { return "An action requiring approval." }
func (*loopApprovalTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (*loopApprovalTool) Validate(raw json.RawMessage) (json.RawMessage, error) { return raw, nil }
func (*loopApprovalTool) RequiresConfirmation(json.RawMessage) (string, bool) {
	return "Approve the scheduled action?", true
}
func (tool *loopApprovalTool) Execute(context.Context, json.RawMessage, func(agent.PartialResult)) (agent.ToolResult, error) {
	tool.calls.Add(1)
	return agent.ToolResult{Content: "executed"}, nil
}

func TestLoopTurnUsesInteractiveToolApproval(t *testing.T) {
	tool := &loopApprovalTool{}
	var calls atomic.Int32
	m := newLoopModel(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"guard-1\",\"type\":\"function\",\"function\":{\"name\":\"guarded_action\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		loopAnswer(w)
	}, tool)
	mgr := scheduler.NewLoopManager()
	p, done, events := startLoopProgram(t, m, mgr)
	_, err := mgr.StartNoMinInterval(m.session.SessionID(), "run guarded action", 20*time.Millisecond, time.Hour, nil)
	require.NoError(t, err)
	waitLoopEvent(t, events, func(msg tea.Msg) bool { _, ok := msg.(ConfirmationDialogMsg); return ok })
	require.Zero(t, tool.calls.Load(), "scheduled tools must wait for approval")
	p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	waitLoopEvent(t, events, func(msg tea.Msg) bool { _, ok := msg.(StreamDoneMsg); return ok })
	mgr.StopAll()
	p.Send(tea.Quit())
	require.NoError(t, <-done)
	require.Zero(t, tool.calls.Load(), "denied scheduled tool must not execute")
	require.False(t, m.confirmation.IsActive())
}

func TestLoopTurnFailureIsVisible(t *testing.T) {
	m := newLoopModel(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "simulated model failure", http.StatusBadRequest)
	})
	mgr := scheduler.NewLoopManager()
	p, done, events := startLoopProgram(t, m, mgr)
	_, err := mgr.StartNoMinInterval(m.session.SessionID(), "check status", 20*time.Millisecond, time.Hour, nil)
	require.NoError(t, err)
	waitLoopEvent(t, events, func(msg tea.Msg) bool { _, ok := msg.(AgentErrorMsg); return ok })
	mgr.StopAll()
	p.Send(tea.Quit())
	require.NoError(t, <-done)
	require.False(t, m.agentBusy)
	require.Error(t, m.err)
	require.Contains(t, m.messages[len(m.messages)-1].Content, "simulated model failure")
}
