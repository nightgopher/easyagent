package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExpertSessionPersistsParent(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	parent, err := s.createSessionRecord(context.Background(), "coding", "", "", "")
	require.NoError(t, err)
	w := businessRequest(t, s, "POST", "/agents", map[string]any{"name": "专家", "instructions": "帮助用户"}, 201)
	var expert struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &expert))
	w = businessRequest(t, s, "POST", "/sessions", map[string]any{"agent_id": expert.ID, "parent_session_id": parent}, 200)
	var child SessionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &child))
	infos, err := s.app.SessionManager().List(context.Background())
	require.NoError(t, err)
	found := false
	for _, info := range infos {
		if info.ID == child.ID {
			require.Equal(t, parent, info.ParentSessionID)
			found = true
		}
	}
	require.True(t, found)
	businessRequest(t, s, "POST", "/sessions", map[string]any{"agent_id": expert.ID, "parent_session_id": "../invalid"}, 400)
}
