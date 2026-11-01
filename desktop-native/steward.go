package main

import (
	"net/url"
	"regexp"
	"strings"
	"time"
)

func (a *preview) openSteward() {
	if !a.Live.Connected {
		return
	}
	origin := a.current()
	a.operation("steward", "POST", "/agents/steward", map[string]any{}, func(v any) {
		if a.current() != origin {
			return
		}
		definition := businessDecode[nativeBusinessAgent](v)
		if definition.ID == "" {
			return
		}
		a.Live.Notice = "管家权限可在设置 → 业务 Agent 中调整；启用会话检索后，可搜索当前主机全部历史（含归档）。"
		a.resource("business-agents", "/agents")
		a.openAgentConversation(definition)
	})
}

func (a *preview) openAgentConversation(definition nativeBusinessAgent) {
	if !internalLinkID.MatchString(definition.ID) {
		a.Live.Error = "Agent 标识无效"
		return
	}
	if definition.Disabled {
		a.Live.Error = "该 Agent 已停用，请在业务 Agent 设置中启用。"
		return
	}
	origin := a.current()
	if origin.RemoteID == "" && origin.AgentID != definition.ID && (origin.Draft != "" || len(origin.Attachments) > 0 || len(origin.FileReferences) > 0) {
		// Persist the original conversation's identity first, without sending its
		// draft to a model or carrying it into a different agent's conversation.
		a.createBoundSession("", origin.Workspace, origin.Application, origin.AgentID, func(*session) { a.openAgentConversation(definition) })
		return
	}
	// Refresh host metadata so a conversation created on another client can be
	// reused too. Do not overwrite this client's in-memory conversation drafts.
	a.operation("agent-session", "GET", "/sessions", nil, func(v any) {
		if a.current() != origin {
			return
		}
		var newest *sessionSearchHit
		var latest time.Time
		for _, raw := range list(v) {
			meta := object(raw)
			stamp, _ := time.Parse(time.RFC3339, sessionActivityTime(meta))
			if str(meta["agent_id"]) != definition.ID || boolean(meta["archived"]) || str(meta["id"]) == "" {
				continue
			}
			if newest == nil || stamp.After(latest) {
				newest = &sessionSearchHit{SessionID: str(meta["id"]), Title: str(meta["title"]), Workspace: str(meta["workspace"])}
				latest = stamp
			}
		}
		a.Search = ""
		if newest != nil {
			a.openSessionSearchHit(*newest)
			a.current().AgentID = definition.ID
			return
		}
		a.startBusinessSession(definition.ID)
	})
}

var internalLinkID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// Links only navigate within the current host. They never execute tools, send
// a prompt, change host or open an arbitrary application URL.
func (a *preview) openAgentLink(href string) bool {
	if !strings.HasPrefix(strings.ToLower(href), "easyagent:") {
		return false
	}
	u, err := url.Parse(href)
	invalid := func() bool { a.Live.Error = "会话或 Agent 链接无效"; return true }
	if err != nil || u.Scheme != "easyagent" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return invalid()
	}
	if !a.Live.Connected {
		a.Live.Error = "连接当前主机后即可打开此链接"
		return true
	}
	if u.Host == "work" && (u.Path == "" || u.Path == "/") && u.RawQuery == "" {
		a.openWork()
		return true
	}
	id := strings.TrimPrefix(u.Path, "/")
	if !internalLinkID.MatchString(id) || (u.Host != "session" && u.Host != "agent") {
		return invalid()
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return invalid()
	}
	for key, entries := range values {
		if u.Host != "session" || (key != "entry" && key != "q") || len(entries) != 1 {
			return invalid()
		}
	}
	entry, keyword := values.Get("entry"), values.Get("q")
	if entry != "" && !internalLinkID.MatchString(entry) || len([]rune(keyword)) > 256 || strings.ContainsAny(keyword, "\x00\r\n") {
		return invalid()
	}
	origin := a.current()
	if u.Host == "agent" {
		a.operation("navigate-agent", "GET", "/agents/"+pathSegment(id), nil, func(v any) {
			if a.current() != origin {
				return
			}
			a.openAgentConversation(businessDecode[nativeBusinessAgent](v))
		})
		return true
	}
	// Resolve current metadata, including archives, before selecting. A link to
	// a deleted session must not manufacture an empty replacement conversation.
	a.operation("navigate-session", "GET", "/sessions", nil, func(v any) {
		if a.current() != origin {
			return
		}
		for _, raw := range list(v) {
			meta := object(raw)
			if str(meta["id"]) != id {
				continue
			}
			hit := sessionSearchHit{SessionID: id, EntryID: entry, Title: str(meta["title"]), Workspace: str(meta["workspace"]), Archived: boolean(meta["archived"])}
			a.openSessionSearchHit(hit)
			a.Live.SessionSearch.TargetQuery = keyword
			return
		}
		a.Live.Error = "原会话不存在或已删除"
	})
	return true
}
