package runtime

import (
	"context"
	"testing"

	"github.com/nightgopher/easyagent/sdk/ai"
	"github.com/nightgopher/easyagent/sdk/ai/providers"
	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/extensions"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
	"github.com/stretchr/testify/require"
)

func TestForkWithOptionsReportsCompactedHistory(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		name := "retained history"
		if repeated {
			name = "repeated compaction"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			manager := sessionmgr.NewManager(t.TempDir())
			cfg := config.Default()
			cfg.Provider, cfg.Workspace = "openai", t.TempDir()
			cfg.SkillMarketEnabled = false
			source, err := NewAgentSession(ctx, AgentSessionOptions{Config: cfg, SkillDirs: []string{t.TempDir()}}, Dependencies{
				SessionMgr: manager, Registry: providers.NewRegistry(), ExtRegistry: extensions.NewRegistry(), Application: &confirmationTestApplication{},
			})
			require.NoError(t, err)
			defer source.Close()
			history := []ai.Message{ai.NewTextUserMessage("old"), ai.AssistantMessage{Text: "old answer"}, ai.NewTextUserMessage("retained"), ai.AssistantMessage{Text: "retained answer"}, ai.NewTextUserMessage("last"), ai.AssistantMessage{Text: "last answer"}}
			for _, message := range history {
				require.NoError(t, source.Session().AppendMessage(ctx, message))
			}
			ids, err := source.Session().BuildContextEntryIDs(ctx)
			require.NoError(t, err)
			require.NoError(t, source.Session().AppendCompactionKeeping(ctx, "first summary", history[2:], nil))
			start := 2
			if repeated {
				require.NoError(t, source.Session().AppendCompactionKeeping(ctx, "second summary", history[4:], nil))
				start = 4
			}
			zero, one, negative := 0, 1, -1
			empty, missing := "", "missing"
			for _, tc := range []struct {
				name    string
				options ForkOptions
				want    []ai.Message
				invalid bool
			}{
				{name: "full", want: history[start:]},
				{name: "empty entry", options: ForkOptions{EntryID: &empty}},
				{name: "before first user", options: ForkOptions{BeforeMessageIndex: &zero}},
				{name: "before next user", options: ForkOptions{BeforeMessageIndex: &one}, want: history[2:4], invalid: repeated},
				{name: "explicit anchor", options: ForkOptions{EntryID: &ids[3]}, want: history[2:4]},
				{name: "missing anchor", options: ForkOptions{EntryID: &missing}, invalid: true},
				{name: "negative index", options: ForkOptions{BeforeMessageIndex: &negative}, invalid: true},
				{name: "conflicting selectors", options: ForkOptions{EntryID: &empty, BeforeMessageIndex: &zero}, invalid: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before, err := manager.List(ctx)
					require.NoError(t, err)
					result, err := source.ForkWithOptions(ctx, tc.options)
					if tc.invalid {
						require.Error(t, err)
						require.Zero(t, result)
						after, err := manager.List(ctx)
						require.NoError(t, err)
						require.Len(t, after, len(before))
						return
					}
					require.NoError(t, err)
					want := tc.want
					if repeated && tc.name == "explicit anchor" {
						// This anchor predates the latest retained view. Its fork
						// can contain more messages than the current source view.
						want = history[:4]
					}
					require.Equal(t, len(want), result.KeptMessages)
					require.Equal(t, len(want)/2, result.KeptUserMessages)
					require.Equal(t, max(0, len(history)-start-len(want)), result.DroppedMessages)
					fork, path, err := manager.Open(ctx, result.ID)
					require.NoError(t, err)
					defer fork.Storage().Close()
					require.Equal(t, result.Path, path)
					actual, err := fork.BuildContext(ctx)
					require.NoError(t, err)
					compactions, err := fork.Compactions(ctx)
					require.NoError(t, err)
					if len(compactions) > 0 {
						actual = actual[1:]
					}
					require.Len(t, actual, len(want))
					if len(want) > 0 {
						require.Equal(t, want, actual)
					}
				})
			}
		})
	}
}
