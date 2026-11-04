package app

import (
	"context"
	"encoding/json"

	"github.com/nightgopher/easyagent/sdk/agent"
	"github.com/nightgopher/easyagent/sdk/runtime"
)

// retiredMusicApplication is the metadata value of sessions created before the
// music application was removed. They stay readable without gaining the default
// application's tools; new sessions cannot select it.
const retiredMusicApplication = "music"

const retiredMusicNotice = "音乐功能已下线，音乐工具不可用。"

// retiredMusicToolNames keeps tool_use blocks in old transcripts resolvable:
// providers may reject a history that references tools absent from the request.
var retiredMusicToolNames = []string{
	"music_search", "music_play", "music_lyrics", "music_detail",
	"music_playlist", "music_recommend", "music_history",
}

type retiredMusicApp struct{}

func (retiredMusicApp) NewSessionExt() runtime.SessionExt { return nil }

func (retiredMusicApp) BuildTools(runtime.ToolBuildOptions) []agent.Tool {
	list := make([]agent.Tool, 0, len(retiredMusicToolNames))
	for _, name := range retiredMusicToolNames {
		list = append(list, retiredMusicTool{name: name})
	}
	return list
}

func (retiredMusicApp) BuildPrompt(runtime.PromptBuildOptions, string, string) string {
	return retiredMusicNotice + "这是一个历史会话，你只能基于已有对话内容回答；不要声称可以搜索或播放音乐。"
}

type retiredMusicTool struct{ name string }

func (t retiredMusicTool) Name() string        { return t.name }
func (t retiredMusicTool) Description() string { return retiredMusicNotice }
func (t retiredMusicTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (t retiredMusicTool) Validate(params json.RawMessage) (json.RawMessage, error) {
	return params, nil
}
func (t retiredMusicTool) Execute(context.Context, json.RawMessage, func(agent.PartialResult)) (agent.ToolResult, error) {
	return agent.ToolResult{Content: retiredMusicNotice, IsError: true}, nil
}

var _ runtime.Application = retiredMusicApp{}
