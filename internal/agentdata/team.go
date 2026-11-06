package agentdata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TeamMember references a saved business agent within a team, with an optional
// note describing the member's role in this team.
type TeamMember struct {
	AgentID string `json:"agent_id"`
	Role    string `json:"role,omitempty"`
}

// Team is a durable group of business agents: a leader that coordinates the
// run and members it can delegate to. Teams only reference agents; member
// capabilities stay defined by each agent's own saved definition.
type Team struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	LeaderID    string       `json:"leader_id"`
	Members     []TeamMember `json:"members"`
	// Workspace optionally names one absolute directory member read access is
	// bound to, instead of each member's own workspace. Explicit team-level
	// configuration only; it never widens a member's own restrictions.
	Workspace string `json:"workspace,omitempty"`
	Disabled  bool   `json:"disabled"`
	Version   int64  `json:"version"`
	UpdatedAt string `json:"updated_at"`
}

const teamSchema = `
CREATE TABLE IF NOT EXISTS business_teams (
	id TEXT PRIMARY KEY,
	definition TEXT NOT NULL,
	version INTEGER NOT NULL
);`

func normalizeTeam(t *Team) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || len([]rune(t.Name)) > 100 {
		return fmt.Errorf("name must contain 1–100 characters")
	}
	t.Description = strings.TrimSpace(t.Description)
	if len(t.Description) > 2000 {
		return fmt.Errorf("description must contain at most 2000 characters")
	}
	t.LeaderID = strings.TrimSpace(t.LeaderID)
	if t.LeaderID == "" {
		return fmt.Errorf("leader_id is required")
	}
	if len(t.Members) == 0 || len(t.Members) > 8 {
		return fmt.Errorf("a team needs 1–8 members")
	}
	seen := map[string]bool{}
	for i, m := range t.Members {
		m.AgentID = strings.TrimSpace(m.AgentID)
		if m.AgentID == "" || m.AgentID == t.LeaderID || seen[m.AgentID] {
			return fmt.Errorf("members must reference distinct agents other than the leader")
		}
		m.Role = strings.TrimSpace(m.Role)
		if len([]rune(m.Role)) > 200 {
			return fmt.Errorf("member role must contain at most 200 characters")
		}
		t.Members[i] = m
		seen[m.AgentID] = true
	}
	if t.Workspace != "" {
		if !filepath.IsAbs(t.Workspace) {
			return fmt.Errorf("workspace must be an absolute path on this host")
		}
		t.Workspace = filepath.Clean(t.Workspace)
	}
	return nil
}

// SaveTeam creates or updates a team. Like SaveAgent it validates references
// (leader and members must exist and not be disabled) and uses optimistic
// versioning; concurrent edits surface as ErrConflict.
func (s *Store) SaveTeam(ctx context.Context, t Team) (Team, error) {
	if err := normalizeTeam(&t); err != nil {
		return Team{}, err
	}
	leader, err := s.GetAgent(ctx, t.LeaderID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Team{}, fmt.Errorf("leader agent %q does not exist", t.LeaderID)
		}
		return Team{}, err
	}
	if leader.Disabled {
		return Team{}, fmt.Errorf("leader agent %q is disabled", t.LeaderID)
	}
	for _, m := range t.Members {
		def, err := s.GetAgent(ctx, m.AgentID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return Team{}, fmt.Errorf("member agent %q does not exist", m.AgentID)
			}
			return Team{}, err
		}
		if def.Disabled {
			return Team{}, fmt.Errorf("member agent %q is disabled", m.AgentID)
		}
	}
	create := t.ID == ""
	if create {
		t.ID, t.Version = "team-"+uuid.NewString(), 0
	}
	previous := t.Version
	t.Version++
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(t)
	if err != nil {
		return Team{}, err
	}
	db, err := s.open(ctx)
	if err != nil {
		return Team{}, err
	}
	defer func() { _ = db.Close() }()
	if create {
		_, err = db.ExecContext(ctx, "INSERT INTO business_teams(id,definition,version) VALUES(?,?,?)", t.ID, string(data), t.Version)
	} else {
		var result sql.Result
		result, err = db.ExecContext(ctx, "UPDATE business_teams SET definition=?,version=? WHERE id=? AND version=?", string(data), t.Version, t.ID, previous)
		if err == nil {
			var count int64
			count, err = result.RowsAffected()
			if err == nil && count == 0 {
				err = ErrConflict
			}
		}
	}
	return t, err
}

func (s *Store) GetTeam(ctx context.Context, id string) (Team, error) {
	db, err := s.open(ctx)
	if err != nil {
		return Team{}, err
	}
	defer func() { _ = db.Close() }()
	var definition string
	if err := db.QueryRowContext(ctx, "SELECT definition FROM business_teams WHERE id=?", id).Scan(&definition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Team{}, ErrNotFound
		}
		return Team{}, err
	}
	var t Team
	if err := json.Unmarshal([]byte(definition), &t); err != nil {
		return Team{}, fmt.Errorf("decode team %s: %w", id, err)
	}
	return t, nil
}

func (s *Store) ListTeams(ctx context.Context) ([]Team, error) {
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, "SELECT definition FROM business_teams ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	teams := []Team{}
	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			return nil, err
		}
		var t Team
		if err := json.Unmarshal([]byte(definition), &t); err != nil {
			return nil, fmt.Errorf("decode team: %w", err)
		}
		teams = append(teams, t)
	}
	return teams, rows.Err()
}
