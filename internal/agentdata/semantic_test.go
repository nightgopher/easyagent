package agentdata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSemanticKnowledgeScopesAndCachesDocuments(t *testing.T) {
	var calls [][]string
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		calls = append(calls, request.Input)
		data := make([]map[string]any, len(request.Input))
		for i := range request.Input {
			data[i] = map[string]any{"index": i, "embedding": []float32{1, 1}}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	defer service.Close()
	ctx := context.Background()
	store := New(t.TempDir())
	store.embeddingHTTPClient = service.Client()
	sourceRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sourceRoot, "a.md"), []byte("# Guide\nHow to restart safely"), 0600))
	privateRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(privateRoot, "private.md"), []byte("private contents"), 0600))
	source, err := store.SaveSource(ctx, KnowledgeSource{Name: "Public", Path: sourceRoot})
	require.NoError(t, err)
	_, err = store.SaveSource(ctx, KnowledgeSource{Name: "Private", Path: privateRoot})
	require.NoError(t, err)
	agent, err := store.SaveAgent(ctx, Agent{Name: "test", Instructions: "role", Knowledge: []KnowledgeBinding{{SourceID: source.ID, Access: "read"}}})
	require.NoError(t, err)
	config := EmbeddingConfig{Enabled: true, Endpoint: service.URL, Model: "test-model", APIKey: "test-key"}
	require.NoError(t, store.SaveEmbeddingConfig(ctx, config))
	hits, err := store.SearchAgentKnowledge(ctx, agent.ID, "恢复服务")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "a.md", hits[0].Path)
	require.Len(t, calls, 2)
	hits, err = store.SearchAgentKnowledge(ctx, agent.ID, "恢复服务")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Len(t, calls, 3, "cached document should not be sent again")
	for _, batch := range calls {
		for _, input := range batch {
			require.NotContains(t, input, "private contents")
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(sourceRoot, "a.md"), []byte("# Guide\nNew restart instructions with changes"), 0600))
	_, err = store.SearchAgentKnowledge(ctx, agent.ID, "恢复服务")
	require.NoError(t, err)
	require.Len(t, calls, 5, "changed document must be embedded again")
	db, err := store.open(ctx)
	require.NoError(t, err)
	var cacheRows int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM knowledge_embedding_vectors`).Scan(&cacheRows))
	require.Equal(t, 1, cacheRows)
	require.NoError(t, db.Close())
	config.Enabled = false
	require.NoError(t, store.SaveEmbeddingConfig(ctx, config))
	db, err = store.open(ctx)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM knowledge_embedding_vectors`).Scan(&cacheRows))
	require.Equal(t, 0, cacheRows)
	require.NoError(t, db.Close())
	require.NoError(t, os.Remove(filepath.Join(sourceRoot, "a.md")))
	hits, err = store.SearchAgentKnowledge(ctx, agent.ID, "恢复服务")
	require.NoError(t, err)
	require.Empty(t, hits)
}
func TestSemanticChunksAndCosine(t *testing.T) {
	chunks := semanticChunks(strings.Repeat("界", 3001))
	require.Len(t, chunks, 3)
	score, err := cosine([]float32{1, 0}, []float32{1, 0})
	require.NoError(t, err)
	require.Equal(t, 1.0, score)
	_, err = cosine([]float32{1}, []float32{1, 2})
	require.Error(t, err)
}

func TestSemanticSearchRejectsOversizedSourceBeforeSending(t *testing.T) {
	calls := 0
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusInternalServerError) }))
	defer service.Close()
	ctx := context.Background()
	store := New(t.TempDir())
	store.embeddingHTTPClient = service.Client()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.md"), []byte(strings.Repeat("x", 400001)), 0600))
	source, err := store.SaveSource(ctx, KnowledgeSource{Name: "Large", Path: root})
	require.NoError(t, err)
	agent, err := store.SaveAgent(ctx, Agent{Name: "test", Instructions: "role", Knowledge: []KnowledgeBinding{{SourceID: source.ID, Access: "read"}}})
	require.NoError(t, err)
	require.NoError(t, store.SaveEmbeddingConfig(ctx, EmbeddingConfig{Enabled: true, Endpoint: service.URL, Model: "test", APIKey: "key"}))
	_, err = store.SearchAgentKnowledge(ctx, agent.ID, "query")
	require.ErrorContains(t, err, "400 KB")
	require.Equal(t, 0, calls)
}

func TestSemanticIndexStopsWhenDisabledDuringRequest(t *testing.T) {
	ctx := context.Background()
	store := New(t.TempDir())
	calls := 0
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var input struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if calls == 2 {
			cfg, err := store.EmbeddingConfig(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			cfg.Enabled = false
			if err = store.SaveEmbeddingConfig(ctx, cfg); err != nil {
				t.Error(err)
				return
			}
		}
		data := make([]map[string]any, len(input.Input))
		for i := range input.Input {
			data[i] = map[string]any{"index": i, "embedding": []float32{1, 1}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer service.Close()
	store.embeddingHTTPClient = service.Client()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.md"), []byte("# knowledge\ncontent"), 0600))
	source, err := store.SaveSource(ctx, KnowledgeSource{Name: "docs", Path: root})
	require.NoError(t, err)
	agent, err := store.SaveAgent(ctx, Agent{Name: "test", Instructions: "role", Knowledge: []KnowledgeBinding{{SourceID: source.ID, Access: "read"}}})
	require.NoError(t, err)
	require.NoError(t, store.SaveEmbeddingConfig(ctx, EmbeddingConfig{Enabled: true, Endpoint: service.URL, Model: "model", APIKey: "key"}))
	_, err = store.SearchAgentKnowledge(ctx, agent.ID, "test query")
	require.ErrorContains(t, err, "settings changed")
	db, err := store.open(ctx)
	require.NoError(t, err)
	defer db.Close()
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM knowledge_embedding_vectors`).Scan(&count))
	require.Zero(t, count)
}
