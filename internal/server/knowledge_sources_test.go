package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgopher/easyagent/internal/agentdata"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeSourceAPIAndReferenceScope(t *testing.T) {
	s, _, _ := newRunTestServer(t)
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "lesson.md"), []byte("# Lesson\n退款流程"), 0600))
	w := businessRequest(t, s, "POST", "/knowledge/sources", map[string]any{"name": "手册", "path": root}, 201)
	var source agentdata.KnowledgeSource
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &source))
	businessRequest(t, s, "GET", "/knowledge/sources", nil, 200)
	businessRequest(t, s, "GET", "/knowledge/sources/"+source.ID, nil, 200)
	businessRequest(t, s, "POST", "/knowledge/sources/"+source.ID+"/reindex", nil, 200)
	w = businessRequest(t, s, "GET", "/knowledge/sources/"+source.ID+"/search?q=退款", nil, 200)
	require.Contains(t, w.Body.String(), "lesson.md")
	businessRequest(t, s, "GET", "/knowledge/sources/"+source.ID+"/document?path=lesson.md", nil, 200)
	a, err := s.app.Agents().SaveAgent(ctx, agentdata.Agent{Name: "Role", Instructions: "Role"})
	require.NoError(t, err)
	w = businessRequest(t, s, "POST", "/sessions", map[string]any{"agent_id": a.ID}, 200)
	var response SessionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	session, err := s.app.LoadSession(ctx, response.ID)
	require.NoError(t, err)
	inputs := promptInputs{Files: []fileReference{{Scope: "knowledge", SourceID: source.ID, Path: "lesson.md", Workspace: session.Workspace()}}}
	_, err = s.buildPromptMessage(session, "read", inputs)
	require.Error(t, err)
	businessRequest(t, s, "GET", "/agents/"+a.ID+"/knowledge/"+source.ID+"/document?path=lesson.md", nil, 400)
	a.Knowledge = []agentdata.KnowledgeBinding{{SourceID: source.ID, Access: "read"}}
	_, err = s.app.Agents().SaveAgent(ctx, a)
	require.NoError(t, err)
	message, err := s.buildPromptMessage(session, "read", inputs)
	require.NoError(t, err)
	require.Contains(t, message.Content[len(message.Content)-1].Text, "退款流程")
	businessRequest(t, s, "GET", "/agents/"+a.ID+"/knowledge?q=退款", nil, 200)
	businessRequest(t, s, "GET", "/agents/"+a.ID+"/knowledge/"+source.ID+"/document?path=lesson.md", nil, 200)
	source.Name = "Updated"
	businessRequest(t, s, "PUT", "/knowledge/sources/"+source.ID, source, 200)
	businessRequest(t, s, "PUT", "/knowledge/sources/"+source.ID, source, 409)
	s.SetAPIKey("owner")
	businessRequest(t, s, "GET", "/knowledge/sources", nil, 401)
}
