package main

import (
	"net/url"
	"regexp"
	"strings"
	"time"
)

// desktopStewardID mirrors internal/agentdata.StewardID; the desktop is a
// separate module and cannot import it.
const desktopStewardID = "agent-steward"

// openSteward resolves the steward's single managed session through the
// server (which also consolidates stray duplicates) and opens it. The
// steward never gets a second conversation.
func (a *preview) openSteward() {
	if !a.Live.Connected {
		return
	}
	origin := a.current()
	if origin.RemoteID == "" && origin.AgentID != desktopStewardID && (origin.Draft != "" || len(origin.Attachments) > 0 || len(origin.FileReferences) > 0) {
		// Persist the original conversation's identity first, without sending its
		// draft to a model or carrying it into the steward conversation.
		a.createBoundSession("", origin.Workspace, origin.Application, origin.AgentID, func(*session) { a.openSteward() })
		return
	}
	a.operation("steward", "POST", "/agents/steward/session", map[string]any{}, func(v any) {
		if a.current() != origin {
			return
		}
		sessionID := str(object(v)["session_id"])
		if sessionID == "" {
			a.Live.Error = "无法打开管家会话"
			return
		}
		a.Live.Notice = "管家是唯一的常驻入口：让它创建任务会话承接具体工作；上下文可在会话内 /clear 清空。"
		a.Search = ""
		a.openSessionSearchHit(sessionSearchHit{SessionID: sessionID})
		a.current().AgentID = desktopStewardID
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
