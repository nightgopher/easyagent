package agentdata

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/nightgopher/easyagent/internal/embeddings"
)

type semanticDocument struct {
	id                                 int64
	path, title, body, stamp, modified string
}
type scoredKnowledge struct {
	hit   KnowledgeHit
	score float64
}

// semanticKnowledge indexes only the requested, already-authorized source. It is
// called after the regular index scan, and only when a host explicitly enabled
// an embedding provider. Errors are surfaced so an incomplete index is never
// silently presented as a complete semantic search.
func (s *Store) semanticKnowledge(ctx context.Context, source KnowledgeSource, query string, limit int, config EmbeddingConfig) ([]KnowledgeHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	db, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var docCount, totalBytes int
	if err = db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(length(CAST(body AS BLOB))),0) FROM knowledge_documents WHERE source_id=?`, source.ID).Scan(&docCount, &totalBytes); err != nil {
		return nil, err
	}
	if docCount > 200 || totalBytes > 400000 {
		return nil, errors.New("semantic search supports up to 200 documents and 400 KB per source; narrow the source directories")
	}
	rows, err := db.QueryContext(ctx, `SELECT id,path,title,body,stamp,modified FROM knowledge_documents WHERE source_id=? ORDER BY id`, source.ID)
	if err != nil {
		return nil, err
	}
	var docs []semanticDocument
	for rows.Next() {
		var d semanticDocument
		if err = rows.Scan(&d.id, &d.path, &d.title, &d.body, &d.stamp, &d.modified); err != nil {
			rows.Close()
			return nil, err
		}
		docs = append(docs, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	client := embeddings.Client{Endpoint: config.Endpoint, Model: config.Model, APIKey: config.APIKey, HTTPClient: s.embeddingHTTPClient}
	vectors, err := client.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	queryVector := vectors[0]
	cacheHash := sha256.Sum256([]byte(config.Endpoint + "\x00" + config.Model))
	cacheKey := hex.EncodeToString(cacheHash[:])
	type prepared struct {
		doc     semanticDocument
		chunks  []string
		vectors [][]float32
		dirty   bool
	}
	var preparedDocs []prepared
	var missingTexts []string
	type position struct{ doc, chunk int }
	var positions []position
	for _, d := range docs {
		chunks := semanticChunks(d.body)
		if len(chunks) == 0 {
			continue
		}
		var encoded string
		err = db.QueryRowContext(ctx, `SELECT vectors FROM knowledge_embedding_vectors WHERE document_id=? AND cache_key=? AND stamp=?`, d.id, cacheKey, d.stamp).Scan(&encoded)
		var cached [][]float32
		if err == nil {
			if json.Unmarshal([]byte(encoded), &cached) != nil || len(cached) != len(chunks) {
				cached = nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		p := prepared{doc: d, chunks: chunks, vectors: cached}
		if cached == nil {
			p.dirty = true
			p.vectors = make([][]float32, len(chunks))
			for i, chunk := range chunks {
				missingTexts = append(missingTexts, chunk)
				positions = append(positions, position{len(preparedDocs), i})
			}
		}
		preparedDocs = append(preparedDocs, p)
	}
	// Bound the size of the persistent vector cache, including previously cached chunks.
	totalChunks := 0
	for _, p := range preparedDocs {
		totalChunks += len(p.chunks)
	}
	if len(queryVector) > 0 && totalChunks > 1000000/len(queryVector) {
		return nil, errors.New("semantic index would exceed one million vector values; narrow the source or use a smaller model")
	}
	for offset := 0; offset < len(missingTexts); offset += 32 {
		var active bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_embedding_config WHERE id=1 AND enabled=1 AND endpoint=? AND model=? AND api_key=?)`, config.Endpoint, config.Model, config.APIKey).Scan(&active); err != nil {
			return nil, err
		}
		if !active {
			return nil, errors.New("embedding settings changed while indexing; retry the search")
		}
		end := min(offset+32, len(missingTexts))
		batch, e := client.Embed(ctx, missingTexts[offset:end])
		if e != nil {
			return nil, e
		}
		for i, v := range batch {
			pos := positions[offset+i]
			preparedDocs[pos.doc].vectors[pos.chunk] = v
		}
	}
	var scored []scoredKnowledge
	for _, p := range preparedDocs {
		d := p.doc
		var currentStamp string
		if err := db.QueryRowContext(ctx, `SELECT stamp FROM knowledge_documents WHERE id=?`, d.id).Scan(&currentStamp); err != nil || currentStamp != d.stamp {
			return nil, errors.New("knowledge changed while indexing; retry the search")
		}
		if p.dirty {
			raw, e := json.Marshal(p.vectors)
			if e != nil {
				return nil, e
			}
			result, e := db.ExecContext(ctx, `INSERT INTO knowledge_embedding_vectors(document_id,cache_key,stamp,vectors) SELECT ?,?,?,? WHERE EXISTS(SELECT 1 FROM knowledge_documents WHERE id=? AND stamp=?) AND EXISTS(SELECT 1 FROM knowledge_embedding_config WHERE id=1 AND enabled=1 AND endpoint=? AND model=? AND api_key=?) ON CONFLICT(document_id,cache_key) DO UPDATE SET stamp=excluded.stamp,vectors=excluded.vectors`, d.id, cacheKey, d.stamp, string(raw), d.id, d.stamp, config.Endpoint, config.Model, config.APIKey)
			if e != nil {
				return nil, e
			}
			n, e := result.RowsAffected()
			if e != nil {
				return nil, e
			}
			if n == 0 {
				return nil, errors.New("knowledge or embedding settings changed while indexing; retry the search")
			}
		}
		best, bestChunk := -2.0, -1
		for i, v := range p.vectors {
			score, e := cosine(queryVector, v)
			if e != nil {
				return nil, e
			}
			if score > best {
				best, bestChunk = score, i
			}
		}
		if bestChunk >= 0 {
			snippet := []rune(p.chunks[bestChunk])
			if len(snippet) > 700 {
				snippet = snippet[:700]
			}
			scored = append(scored, scoredKnowledge{KnowledgeHit{SourceID: source.ID, SourceName: source.Name, Path: d.path, Title: d.title, Snippet: string(snippet), Modified: d.modified}, best})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].hit.Path < scored[j].hit.Path
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	hits := make([]KnowledgeHit, 0, len(scored))
	for _, v := range scored {
		hits = append(hits, v.hit)
	}
	return hits, nil
}

func semanticChunks(text string) []string {
	runes := []rune(strings.TrimSpace(text))
	var chunks []string
	for len(runes) > 0 {
		n := min(1500, len(runes))
		chunk := strings.TrimSpace(string(runes[:n]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		runes = runes[n:]
	}
	return chunks
}
func cosine(a, b []float32) (float64, error) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, errors.New("embedding dimensions do not match")
	}
	var dot, na, nb float64
	for i, x := range a {
		y := b[i]
		dot += float64(x) * float64(y)
		na += float64(x) * float64(x)
		nb += float64(y) * float64(y)
	}
	if na == 0 || nb == 0 {
		return 0, fmt.Errorf("embedding vector has zero length")
	}
	return dot / math.Sqrt(na*nb), nil
}
