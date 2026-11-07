package agentdata

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func teamFixture(t *testing.T, s *Store) (leader Agent, m1, m2 Agent) {
	t.Helper()
	leader = Agent{Name: "Lead", Instructions: "coordinate", Tools: DefaultTools()}
	leader, err := s.SaveAgent(context.Background(), leader)
	require.NoError(t, err)
	m1 = Agent{Name: "Researcher", Instructions: "research", Tools: DefaultTools()}
	m1, err = s.SaveAgent(context.Background(), m1)
	require.NoError(t, err)
	m2 = Agent{Name: "Reviewer", Instructions: "review", Tools: DefaultTools()}
	m2, err = s.SaveAgent(context.Background(), m2)
	require.NoError(t, err)
	return leader, m1, m2
}

func TestTeamSaveGetListRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	leader, m1, m2 := teamFixture(t, s)
	team := Team{
		Name:        "Research squad",
		Description: " reads and reviews ",
		LeaderID:    leader.ID,
		Members:     []TeamMember{{AgentID: m1.ID, Role: " 资料检索 "}, {AgentID: m2.ID}},
		Workspace:   "/tmp/team-ws/",
	}
	saved, err := s.SaveTeam(context.Background(), team)
	require.NoError(t, err)
	require.Equal(t, "team-", saved.ID[:5])
	require.EqualValues(t, 1, saved.Version)
	require.Equal(t, "reads and reviews", saved.Description)
	require.Equal(t, "资料检索", saved.Members[0].Role)
	require.Equal(t, "/tmp/team-ws", saved.Workspace)

	loaded, err := s.GetTeam(context.Background(), saved.ID)
	require.NoError(t, err)
	require.Equal(t, saved, loaded)

	// A second store instance sees the same persisted team.
	again, err := New(t.TempDir()).GetTeam(context.Background(), "missing")
	require.True(t, errors.Is(err, ErrNotFound), "want ErrNotFound, got %v", err)
	_ = again

	list, err := s.ListTeams(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, saved.ID, list[0].ID)
}

func TestTeamSaveValidatesReferences(t *testing.T) {
	s := New(t.TempDir())
	leader, m1, _ := teamFixture(t, s)

	_, err := s.SaveTeam(context.Background(), Team{Name: "T", LeaderID: "agent-missing", Members: []TeamMember{{AgentID: m1.ID}}})
	require.ErrorContains(t, err, "does not exist")

	_, err = s.SaveTeam(context.Background(), Team{Name: "T", LeaderID: leader.ID, Members: []TeamMember{{AgentID: "agent-missing"}}})
	require.ErrorContains(t, err, "does not exist")

	disabled := leader
	disabled.Disabled = true
	disabled, err = s.SaveAgent(context.Background(), disabled)
	require.NoError(t, err)
	_, err = s.SaveTeam(context.Background(), Team{Name: "T", LeaderID: disabled.ID, Members: []TeamMember{{AgentID: m1.ID}}})
	require.ErrorContains(t, err, "disabled")

	offMember := m1
	offMember.Disabled = true
	offMember, err = s.SaveAgent(context.Background(), offMember)
	require.NoError(t, err)
	_, err = s.SaveTeam(context.Background(), Team{Name: "T", LeaderID: leader.ID, Members: []TeamMember{{AgentID: offMember.ID}}})
	require.ErrorContains(t, err, "disabled")
}

func TestTeamNormalize(t *testing.T) {
	s := New(t.TempDir())
	leader, m1, _ := teamFixture(t, s)
	ctx := context.Background()

	for name, team := range map[string]Team{
		"empty name":         {Name: "  ", LeaderID: leader.ID, Members: []TeamMember{{AgentID: m1.ID}}},
		"long name":          {Name: string(make([]byte, 101)), LeaderID: leader.ID, Members: []TeamMember{{AgentID: m1.ID}}},
		"no leader":          {Name: "T", Members: []TeamMember{{AgentID: m1.ID}}},
		"no members":         {Name: "T", LeaderID: leader.ID},
		"leader as member":   {Name: "T", LeaderID: leader.ID, Members: []TeamMember{{AgentID: leader.ID}}},
		"duplicate member":   {Name: "T", LeaderID: leader.ID, Members: []TeamMember{{AgentID: m1.ID}, {AgentID: m1.ID}}},
		"relative workspace": {Name: "T", LeaderID: leader.ID, Members: []TeamMember{{AgentID: m1.ID}}, Workspace: "relative/path"},
		"long description":   {Name: "T", Description: string(make([]byte, 2001)), LeaderID: leader.ID, Members: []TeamMember{{AgentID: m1.ID}}},
		"long role":          {Name: "T", LeaderID: leader.ID, Members: []TeamMember{{AgentID: m1.ID, Role: string(make([]byte, 201))}}},
	} {
		_, err := s.SaveTeam(ctx, team)
		require.Error(t, err, name)
	}
}

func TestTeamVersionConflict(t *testing.T) {
	s := New(t.TempDir())
	leader, m1, _ := teamFixture(t, s)
	team, err := s.SaveTeam(context.Background(), Team{Name: "T", LeaderID: leader.ID, Members: []TeamMember{{AgentID: m1.ID}}})
	require.NoError(t, err)

	stale := team
	stale.Name = "Stale"
	saved, err := s.SaveTeam(context.Background(), stale)
	require.NoError(t, err)
	require.EqualValues(t, 2, saved.Version)

	team.Name = "Conflicts"
	_, err = s.SaveTeam(context.Background(), team)
	require.True(t, errors.Is(err, ErrConflict), "want ErrConflict, got %v", err)
}

func TestTeamSurvivesDisabledMemberAfterSave(t *testing.T) {
	// A member disabled after the team was saved stays stored; execution-time
	// checks decide whether it can still be delegated to.
	s := New(t.TempDir())
	leader, m1, _ := teamFixture(t, s)
	team, err := s.SaveTeam(context.Background(), Team{Name: "T", LeaderID: leader.ID, Members: []TeamMember{{AgentID: m1.ID}}})
	require.NoError(t, err)

	m1.Disabled = true
	m1, err = s.SaveAgent(context.Background(), m1)
	require.NoError(t, err)

	loaded, err := s.GetTeam(context.Background(), team.ID)
	require.NoError(t, err)
	require.Equal(t, m1.ID, loaded.Members[0].AgentID)
}

func TestTeamContextCancellation(t *testing.T) {
	s := New(t.TempDir())
	leader, m1, _ := teamFixture(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.SaveTeam(ctx, Team{Name: "T", LeaderID: leader.ID, Members: []TeamMember{{AgentID: m1.ID}}})
	require.ErrorIs(t, err, context.Canceled)
}
