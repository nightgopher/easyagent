package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/stretchr/testify/require"
)

func TestAgentMemoryPublicationAPI(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	ctx := context.Background()
	store := s.app.Agents()
	src, err := store.SaveSource(ctx, agentdata.KnowledgeSource{Name: "guide", Path: t.TempDir(), Writable: true})
	require.NoError(t, err)
	a, err := store.SaveAgent(ctx, agentdata.Agent{Name: "support", Instructions: "role", MemoryEnabled: true, Knowledge: []agentdata.KnowledgeBinding{{SourceID: src.ID, Access: "write"}}})
	require.NoError(t, err)
	m, err := store.SaveMemory(ctx, a.ID, "", "user", "", agentdata.Memory{Key: "fact", Content: "private"})
	require.NoError(t, err)
	path := "/agents/" + a.ID + "/memories/" + m.ID + "/publish"
	input := map[string]any{"source_id": src.ID, "directory": ".", "title": "Reviewed", "content": "Public guide", "version": m.Version}
	w := businessRequest(t, s, "POST", path, input, 200)
	var result map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Equal(t, true, result["saved"])
	businessRequest(t, s, "POST", path, input, 200)
	businessRequest(t, s, "POST", "/agents/missing/memories/"+m.ID+"/publish", input, 404)
	s.SetAPIKey("owner")
	businessRequest(t, s, "POST", path, input, 401)
}
