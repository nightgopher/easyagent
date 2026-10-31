package tools

import "github.com/nightgopher/easyagent/sdk/agent"

func (*WebSearchTool) DiscoveryInfo() agent.ToolDiscoveryInfo {
	return agent.ToolDiscoveryInfo{Deferred: true, SourceID: "builtin:web", SourceLabel: "Web search", SearchTerms: []string{"网页", "搜索", "互联网", "联网", "新闻", "最新"}}
}

func (*WebFetchTool) DiscoveryInfo() agent.ToolDiscoveryInfo {
	return agent.ToolDiscoveryInfo{Deferred: true, SourceID: "builtin:web", SourceLabel: "Web", SearchTerms: []string{"网页", "链接", "读取网址", "下载", "URL", "fetch"}}
}

func (*SkillMarketTool) DiscoveryInfo() agent.ToolDiscoveryInfo {
	return agent.ToolDiscoveryInfo{Deferred: true, SourceID: "builtin:skill-market", SourceLabel: "Skill marketplace", SearchTerms: []string{"技能", "市场", "安装技能", "skill", "marketplace"}}
}
