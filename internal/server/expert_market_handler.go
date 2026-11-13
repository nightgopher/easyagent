package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/sdk/expertmarket"
	"github.com/nightgopher/easyagent/sdk/skill"
)

// ── Expert market REST endpoints ────────────────────────────────────────────
//
// Templates proxy the marketplace's remote expert catalog (the same content
// source as the skill store). Install converts a template into business
// agent/team rows synchronously — local SQLite writes are fast, so unlike the
// skill market there are no background jobs here. Installed skills that the
// host does not have are reported back as missing_skills instead of failing,
// mirroring the runtime behavior of silently skipping unknown bindings.

// expertInstallMu serializes installs and ledger writes within this process.
var expertInstallMu sync.Mutex

type expertInstallRecord struct {
	TemplateID  string   `json:"template_id"`
	Kind        string   `json:"kind"`
	AgentIDs    []string `json:"agent_ids,omitempty"`
	TeamID      string   `json:"team_id,omitempty"`
	InstalledAt string   `json:"installed_at"`
}

type expertInstallLedger struct {
	Items []expertInstallRecord `json:"items"`
}

// registerExpertMarketRoutes adds expert-market endpoints to the mux.
func (s *Server) registerExpertMarketRoutes(mux *http.ServeMux) {
	if s.app.ExpertMarket() == nil {
		return // disabled via EA_EXPERT_MARKET=0
	}
	mux.HandleFunc("GET /experts/market/templates", s.expertMarketTemplates)
	mux.HandleFunc("POST /experts/market/install", s.expertMarketInstall)
}

// expertMarketTemplates serves GET /experts/market/templates: the sanitized
// remote catalog plus local install records. An empty catalog is reported with
// an error hint instead of a blank list so clients can offer a retry.
func (s *Server) expertMarketTemplates(w http.ResponseWriter, r *http.Request) {
	source := s.app.ExpertMarketSource()
	source.Refresh(r.Context())
	items, meta := source.Current()

	records, err := readExpertInstalls(s.app.Config().DataDir)
	if err != nil {
		records = expertInstallLedger{Items: []expertInstallRecord{}}
	}
	out := map[string]any{
		"items":     items,
		"meta":      meta,
		"installed": records.Items,
	}
	if len(items) == 0 {
		out["error"] = "专家目录暂不可用，请稍后重试"
	}
	writeJSON(w, out)
}

type expertMarketInstallRequest struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Locale string `json:"locale,omitempty"` // zh (default) | en
}

// expertMarketInstall serves POST /experts/market/install. Each install
// creates fresh agents (and, for teams, the team row referencing them);
// repeated installs intentionally produce new IDs, matching the reference
// client's template semantics.
func (s *Server) expertMarketInstall(w http.ResponseWriter, r *http.Request) {
	var req expertMarketInstallRequest
	if !decodeAgentJSON(w, r, &req) {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	locale := req.Locale
	if locale == "" {
		locale = "zh"
	}
	kind := expertmarket.Kind(req.Kind)
	if req.ID == "" || (kind != expertmarket.KindExpert && kind != expertmarket.KindTeam) {
		writeError(w, http.StatusBadRequest, "id 和 kind（expert|team）必须填写")
		return
	}
	if locale != "zh" && locale != "en" {
		writeError(w, http.StatusBadRequest, "locale 仅支持 zh 或 en")
		return
	}

	// Installs are quick but touch the ledger; keep them serialized.
	expertInstallMu.Lock()
	defer expertInstallMu.Unlock()

	source := s.app.ExpertMarketSource()
	source.Refresh(r.Context())
	items, _ := source.Current()
	if len(items) == 0 {
		writeError(w, http.StatusBadGateway, "专家目录暂不可用，请稍后重试")
		return
	}
	var tpl *expertmarket.Template
	for i := range items {
		if items[i].ID == req.ID && items[i].Kind == kind {
			tpl = &items[i]
			break
		}
	}
	if tpl == nil {
		writeError(w, http.StatusNotFound, "目录中未找到该模板")
		return
	}

	store := s.app.Agents()
	loaded := s.loadedSkillNames()
	record := expertInstallRecord{
		TemplateID:  tpl.ID,
		Kind:        string(kind),
		InstalledAt: time.Now().UTC().Format(time.RFC3339),
	}

	var response map[string]any
	switch kind {
	case expertmarket.KindExpert:
		agent, err := installExpertTemplate(r.Context(), store, *tpl, locale)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "安装专家失败："+err.Error())
			return
		}
		record.AgentIDs = []string{agent.ID}
		response = map[string]any{
			"installed":      true,
			"agents":         []agentdata.Agent{agent},
			"missing_skills": missingSkills(tpl.Skills, loaded),
		}
	case expertmarket.KindTeam:
		byID := map[string]expertmarket.Template{}
		for _, item := range items {
			if item.Kind == expertmarket.KindExpert {
				byID[item.ID] = item
			}
		}
		roles := append([]string{tpl.Leader}, tpl.Members...)
		created := make([]agentdata.Agent, 0, len(roles))
		byTemplate := map[string]agentdata.Agent{}
		var boundSkills []string
		for _, id := range roles {
			member, ok := byID[id]
			if !ok {
				writeError(w, http.StatusBadGateway, "团队引用的专家模板缺失："+id)
				return
			}
			agent, err := installExpertTemplate(r.Context(), store, member, locale)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "安装团队成员失败："+err.Error())
				return
			}
			created = append(created, agent)
			byTemplate[id] = agent
			boundSkills = append(boundSkills, member.Skills...)
		}
		members := make([]agentdata.TeamMember, 0, len(tpl.Members))
		for _, id := range tpl.Members {
			members = append(members, agentdata.TeamMember{AgentID: byTemplate[id].ID})
		}
		team, err := store.SaveTeam(r.Context(), agentdata.Team{
			Name:        localizedText(tpl.Name, locale),
			Description: localizedText(tpl.Description, locale),
			LeaderID:    byTemplate[tpl.Leader].ID,
			Members:     members,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "保存专家团失败："+err.Error())
			return
		}
		for _, agent := range created {
			record.AgentIDs = append(record.AgentIDs, agent.ID)
		}
		record.TeamID = team.ID
		response = map[string]any{
			"installed":      true,
			"agents":         created,
			"team":           team,
			"missing_skills": missingSkills(boundSkills, loaded),
		}
	}

	if err := appendExpertInstalls(s.app.Config().DataDir, record); err != nil {
		// The install itself succeeded; the badge ledger is best-effort.
		response["ledger_error"] = err.Error()
	}
	writeJSON(w, response)
}

// installExpertTemplate converts a sanitized expert template into a business
// agent row. Templates carry no model/workspace of their own, so agents use
// the defaults (per-agent workspace, default tools).
func installExpertTemplate(ctx context.Context, store *agentdata.Store, t expertmarket.Template, locale string) (agentdata.Agent, error) {
	return store.SaveAgent(ctx, agentdata.Agent{
		Name:          localizedText(t.Name, locale),
		Instructions:  expertInstructions(t, locale),
		Tools:         agentdata.DefaultTools(),
		Skills:        append([]string(nil), t.Skills...),
		MemoryEnabled: true,
	})
}

// localizedText picks the requested language, defaulting to zh.
func localizedText(t expertmarket.LocalizedText, locale string) string {
	if locale == "en" {
		return t.En
	}
	return t.Zh
}

// expertInstructions folds the template description into the agent
// instructions: Agent has no description field, so the one-line summary
// introduces the role prompt.
func expertInstructions(t expertmarket.Template, locale string) string {
	desc := localizedText(t.Description, locale)
	body := localizedText(t.Instructions, locale)
	if desc == "" {
		return body
	}
	if body == "" {
		return desc
	}
	return desc + "\n\n" + body
}

// loadedSkillNames lists every skill name loadable from the host's global
// skill directories — the same sources live sessions load from.
func (s *Server) loadedSkillNames() map[string]bool {
	names := map[string]bool{}
	for _, root := range s.app.GlobalSkillDirs() {
		for _, sk := range skill.LoadFromDirs(root).Skills {
			names[sk.Name] = true
		}
	}
	return names
}

// missingSkills reports bound skills the host cannot load. Missing bindings
// are not fatal: the runtime silently skips them, so installation succeeds
// and the client just gets a heads-up.
func missingSkills(skills []string, loaded map[string]bool) []string {
	missing := []string{}
	for _, name := range skills {
		if !loaded[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// ─── Install ledger ──────────────────────────────────────────────────────────

// expertLedgerPath is where install records live for the "已安装" badge.
func expertLedgerPath(dataDir string) string {
	return filepath.Join(dataDir, "expert-market", "installed.json")
}

func readExpertInstalls(dataDir string) (expertInstallLedger, error) {
	data, err := os.ReadFile(expertLedgerPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return expertInstallLedger{Items: []expertInstallRecord{}}, nil
		}
		return expertInstallLedger{}, err
	}
	var ledger expertInstallLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return expertInstallLedger{}, err
	}
	if ledger.Items == nil {
		ledger.Items = []expertInstallRecord{}
	}
	return ledger, nil
}

func appendExpertInstalls(dataDir string, record expertInstallRecord) error {
	ledger, err := readExpertInstalls(dataDir)
	if err != nil {
		ledger = expertInstallLedger{Items: []expertInstallRecord{}}
	}
	ledger.Items = append(ledger.Items, record)
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	path := expertLedgerPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
