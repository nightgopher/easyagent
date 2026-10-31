package main

import (
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type nativeKnowledgeBinding struct {
	SourceID string `json:"source_id"`
	Access   string `json:"access"`
}
type nativeBusinessAgent struct {
	ID            string                   `json:"id"`
	Name          string                   `json:"name"`
	Instructions  string                   `json:"instructions"`
	Model         string                   `json:"model"`
	Workspace     string                   `json:"workspace"`
	Tools         []string                 `json:"tools"`
	Skills        []string                 `json:"skills"`
	Knowledge     []nativeKnowledgeBinding `json:"knowledge"`
	MemoryEnabled bool                     `json:"memory_enabled"`
	Disabled      bool                     `json:"disabled"`
	Version       int64                    `json:"version"`
}
type nativeKnowledgeSource struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Directories []string `json:"directories"`
	Writable    bool     `json:"writable"`
	Disabled    bool     `json:"disabled"`
	Version     int64    `json:"version"`
	Available   bool     `json:"available"`
	Problem     string   `json:"problem"`
}
type nativeAgentMemory struct {
	ID            string `json:"id"`
	AgentID       string `json:"agent_id"`
	Key           string `json:"key"`
	Content       string `json:"content"`
	Scope         string `json:"scope"`
	Workspace     string `json:"workspace"`
	ExpiresAt     string `json:"expires_at"`
	SourceKind    string `json:"source_kind"`
	SourceSession string `json:"source_session"`
	UpdatedAt     string `json:"updated_at"`
	Version       int64  `json:"version"`
	Expired       bool   `json:"expired"`
}
type businessSettingsState struct {
	AgentEditor, SourceEditor, MemoryEditor, Publishing, Advanced   bool
	Agent                                                           nativeBusinessAgent
	Source                                                          nativeKnowledgeSource
	Memory                                                          nativeAgentMemory
	Baseline, ExtraTools, SkillNames, Directories                   string
	Tab, AgentQuery, MemoryAgentID, MemoryQuery, AppliedMemoryQuery string
	MemoryOffset                                                    int
	BrowseSourceID, KnowledgeQuery, DocumentPath                    string
	PublishSourceID, PublishDirectory, PublishTitle, PublishContent string
}

var businessBaseTools = []string{"read", "write", "ask_user_question", "local_time", "search_tools", "web_fetch", "web_search"}
var businessDefaultTools = []string{"read", "write", "ask_user_question", "local_time", "search_tools"}

func businessDecode[T any](v any) T {
	var out T
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	return out
}
func businessLines(v string) []string {
	out := []string{}
	for _, name := range strings.FieldsFunc(v, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' || r == '，' }) {
		name = strings.TrimSpace(name)
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}
func (s *businessSettingsState) snapshot() string {
	var v any
	switch {
	case s.AgentEditor:
		v = struct {
			Agent         nativeBusinessAgent
			Extra, Skills string
		}{s.Agent, s.ExtraTools, s.SkillNames}
	case s.SourceEditor:
		v = struct {
			Source      nativeKnowledgeSource
			Directories string
		}{s.Source, s.Directories}
	case s.MemoryEditor:
		v = s.Memory
	case s.Publishing:
		v = []string{s.PublishSourceID, s.PublishDirectory, s.PublishTitle, s.PublishContent}
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func (s *businessSettingsState) dirty() bool {
	return (s.AgentEditor || s.SourceEditor || s.MemoryEditor || s.Publishing) && s.snapshot() != s.Baseline
}
func (s *businessSettingsState) closeEditors() {
	s.AgentEditor = false
	s.SourceEditor = false
	s.MemoryEditor = false
	s.Publishing = false
	s.Baseline = ""
}
func (a *preview) businessNavigate(next func()) {
	if a.Live.Loading["business-save"] {
		return
	}
	if a.Settings.Business.dirty() {
		a.Live.Confirm = &nativeConfirmation{"放弃未保存的修改？", "当前修改尚未保存，继续将丢弃这些修改。", func() { a.Settings.Business.closeEditors(); next() }}
		return
	}
	next()
}
func (a *preview) businessAgents() []nativeBusinessAgent {
	return businessDecode[[]nativeBusinessAgent](object(a.Live.Data["business-agents"])["agents"])
}
func (a *preview) knowledgeSources() []nativeKnowledgeSource {
	return businessDecode[[]nativeKnowledgeSource](object(a.Live.Data["knowledge-sources"])["sources"])
}
func (a *preview) businessAgentName(id string) string {
	for _, v := range a.businessAgents() {
		if v.ID == id {
			return v.Name
		}
	}
	return id
}
func (a *preview) refreshBusiness() {
	a.resource("business-agents", "/agents")
	a.resource("knowledge-sources", "/knowledge/sources")
}
func (a *preview) editBusinessAgent(v nativeBusinessAgent) {
	delete(a.Live.ResourceErrors, "business-save")
	s := &a.Settings.Business
	s.closeEditors()
	s.AgentEditor = true
	s.Advanced = false
	if v.ID == "" {
		v.Tools = slices.Clone(businessDefaultTools)
		v.Skills = []string{}
		v.Knowledge = []nativeKnowledgeBinding{}
		v.MemoryEnabled = true
	}
	s.Agent = v
	s.ExtraTools = ""
	for _, tool := range v.Tools {
		if !slices.Contains(businessBaseTools, tool) {
			s.ExtraTools += tool + "\n"
		}
	}
	s.SkillNames = strings.Join(v.Skills, "\n")
	s.Baseline = s.snapshot()
	a.loadLocalSkills()
}
func (a *preview) saveBusinessAgent() {
	s := &a.Settings.Business
	v := s.Agent
	v.Tools = []string{}
	for _, name := range businessBaseTools {
		if slices.Contains(s.Agent.Tools, name) {
			v.Tools = append(v.Tools, name)
		}
	}
	for _, name := range businessLines(s.ExtraTools) {
		if !slices.Contains(v.Tools, name) {
			v.Tools = append(v.Tools, name)
		}
	}
	v.Skills = businessLines(s.SkillNames)
	if v.Knowledge == nil {
		v.Knowledge = []nativeKnowledgeBinding{}
	}
	method, path := "POST", "/agents"
	if v.ID != "" {
		method, path = "PUT", "/agents/"+url.PathEscape(v.ID)
	}
	a.operation("business-save", method, path, v, func(any) {
		s.closeEditors()
		a.Settings.message("Agent 已保存；权限与说明将在下次执行时生效。")
		a.refreshBusiness()
	})
}
func (a *preview) editKnowledgeSource(v nativeKnowledgeSource) {
	delete(a.Live.ResourceErrors, "business-save")
	s := &a.Settings.Business
	s.closeEditors()
	s.SourceEditor = true
	if len(v.Directories) == 0 {
		v.Directories = []string{"."}
	}
	s.Source = v
	s.Directories = strings.Join(v.Directories, "\n")
	s.Baseline = s.snapshot()
}
func (a *preview) saveKnowledgeSource() {
	s := &a.Settings.Business
	v := s.Source
	body := map[string]any{"id": v.ID, "name": v.Name, "path": v.Path, "directories": businessLines(s.Directories), "writable": v.Writable, "disabled": v.Disabled, "version": v.Version}
	method, path := "POST", "/knowledge/sources"
	if v.ID != "" {
		method, path = "PUT", path+"/"+url.PathEscape(v.ID)
	}
	a.operation("business-save", method, path, body, func(any) {
		s.closeEditors()
		a.Settings.message("知识来源已保存。可检查目录与检索结果后绑定给 Agent。")
		a.refreshBusiness()
	})
}
func (a *preview) loadAgentMemories() {
	s := &a.Settings.Business
	if s.MemoryAgentID == "" {
		return
	}
	a.resource("agent-memories", "/agents/"+url.PathEscape(s.MemoryAgentID)+"/memories"+query(map[string]string{"q": s.AppliedMemoryQuery, "offset": strconv.Itoa(s.MemoryOffset)}))
}
func (a *preview) selectMemoryAgent(id string) {
	s := &a.Settings.Business
	s.closeEditors()
	s.MemoryAgentID = id
	s.MemoryOffset = 0
	delete(a.Live.Data, "agent-memories")
	a.loadAgentMemories()
}
func (a *preview) editAgentMemory(v nativeAgentMemory) {
	delete(a.Live.ResourceErrors, "business-save")
	s := &a.Settings.Business
	s.closeEditors()
	s.MemoryEditor = true
	if v.Scope == "" {
		v.Scope = "agent"
	}
	s.Memory = v
	s.Baseline = s.snapshot()
}
func (a *preview) saveAgentMemory() {
	s := &a.Settings.Business
	m := s.Memory
	path := "/agents/" + url.PathEscape(s.MemoryAgentID) + "/memories"
	method := "POST"
	if m.ID != "" {
		method = "PUT"
		path += "/" + url.PathEscape(m.ID)
	}
	body := map[string]any{"key": m.Key, "content": m.Content, "scope": m.Scope, "expires_at": m.ExpiresAt, "version": m.Version}
	a.operation("business-save", method, path, body, func(any) { s.closeEditors(); a.Settings.message("记忆已保存。"); a.loadAgentMemories() })
}
func (a *preview) deleteAgentMemory(m nativeAgentMemory) {
	id := a.Settings.Business.MemoryAgentID
	a.Live.Confirm = &nativeConfirmation{"删除这条记忆？", m.Key + "：" + m.Content, func() {
		a.operation("business-save", "DELETE", "/agents/"+url.PathEscape(id)+"/memories/"+url.PathEscape(m.ID), map[string]any{"version": m.Version}, func(any) { a.loadAgentMemories() })
	}}
}
func (a *preview) browseKnowledge(id string) {
	s := &a.Settings.Business
	s.BrowseSourceID = id
	s.KnowledgeQuery = ""
	s.DocumentPath = ""
	delete(a.Live.Data, "knowledge-results")
	a.searchBusinessKnowledge()
}
func (a *preview) searchBusinessKnowledge() {
	s := &a.Settings.Business
	a.resource("knowledge-results", "/knowledge/sources/"+url.PathEscape(s.BrowseSourceID)+"/search"+query(map[string]string{"q": s.KnowledgeQuery}))
}
func (a *preview) writableMemorySources() []nativeKnowledgeSource {
	out := []nativeKnowledgeSource{}
	id := a.Settings.Business.MemoryAgentID
	for _, v := range a.businessAgents() {
		if v.ID != id || v.Disabled {
			continue
		}
		for _, b := range v.Knowledge {
			if b.Access != "write" {
				continue
			}
			for _, src := range a.knowledgeSources() {
				if src.ID == b.SourceID && src.Writable && !src.Disabled {
					out = append(out, src)
				}
			}
		}
	}
	return out
}
func (a *preview) prepareMemoryPublication(m nativeAgentMemory) {
	delete(a.Live.ResourceErrors, "business-save")
	s := &a.Settings.Business
	s.closeEditors()
	s.Publishing = true
	s.Memory = m
	s.PublishTitle = m.Key
	s.PublishContent = m.Content
	s.PublishSourceID = ""
	s.PublishDirectory = ""
	choices := a.writableMemorySources()
	if len(choices) > 0 {
		s.PublishSourceID = choices[0].ID
		s.PublishDirectory = choices[0].Directories[0]
	}
	s.Baseline = s.snapshot()
}
func (a *preview) publishAgentMemory() {
	s := &a.Settings.Business
	body := map[string]any{"source_id": s.PublishSourceID, "directory": s.PublishDirectory, "title": s.PublishTitle, "content": s.PublishContent, "version": s.Memory.Version}
	a.operation("business-save", "POST", "/agents/"+url.PathEscape(s.MemoryAgentID)+"/memories/"+url.PathEscape(s.Memory.ID)+"/publish", body, func(v any) {
		s.closeEditors()
		m := object(v)
		a.Settings.message("已保存知识文档：" + str(m["path"]))
		if warning := str(m["warning"]); warning != "" {
			a.Settings.message(warning)
		}
	})
}

func (a *preview) sessionApplicationLabel(s *session) string {
	if s.AgentID != "" {
		return a.businessAgentName(s.AgentID)
	}
	return strings.ToUpper(s.Application)
}
