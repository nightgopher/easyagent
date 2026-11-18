package server

import (
	"encoding/json"
	"net/http"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/nightgopher/easyagent/sdk/sessionmgr"
)

// stewardSession handles POST /agents/steward/session. The steward owns
// exactly one live session: resolve the latest non-archived one, archive
// stray duplicates from before this rule existed, and create the session on
// first use. Clients (desktop/mobile/watchdog) call this instead of
// POST /sessions with the steward agent id.
func (s *Server) stewardSession(w http.ResponseWriter, r *http.Request) {
	definition, err := s.app.Agents().EnsureSteward(r.Context())
	if err == nil && definition.Disabled {
		err = agentdata.ErrDisabled
	}
	if err != nil {
		agentDataError(w, err)
		return
	}

	sessions, err := s.app.SessionManager().List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var latest sessionmgr.SessionInfo
	found := false
	strays := []string{}
	for _, info := range sessions {
		if info.AgentID != agentdata.StewardID || info.Archived {
			continue
		}
		// Session ids are sess_<UnixNano>, so id order breaks LastActive ties
		// deterministically toward the most recently created session.
		newer := info.LastActive > latest.LastActive ||
			(info.LastActive == latest.LastActive && info.ID > latest.ID)
		if !found || newer {
			if found {
				strays = append(strays, latest.ID)
			}
			latest, found = info, true
		} else {
			strays = append(strays, info.ID)
		}
	}

	created := false
	if !found {
		sessionID, err := s.createSessionRecord(r.Context(), "business", agentdata.StewardID, "", "")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		latest.ID, created = sessionID, true
	} else {
		archived := true
		for _, id := range strays {
			_ = s.app.SessionManager().UpdatePreferences(id, sessionmgr.PreferencePatch{Archived: &archived})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session_id": latest.ID,
		"created":    created,
	})
}
