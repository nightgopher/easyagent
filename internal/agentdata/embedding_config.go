package agentdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// EmbeddingConfig belongs to the host. APIKey is never returned by the HTTP API.
type EmbeddingConfig struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key,omitempty"`
	HasKey   bool   `json:"has_key"`
	ClearKey bool   `json:"clear_key,omitempty"`
}

func (s *Store) EmbeddingConfig(ctx context.Context) (EmbeddingConfig, error) {
	db, err := s.open(ctx)
	if err != nil {
		return EmbeddingConfig{}, err
	}
	defer db.Close()
	var c EmbeddingConfig
	err = db.QueryRowContext(ctx, `SELECT enabled,endpoint,model,api_key FROM knowledge_embedding_config WHERE id=1`).Scan(&c.Enabled, &c.Endpoint, &c.Model, &c.APIKey)
	if errors.Is(err, sql.ErrNoRows) {
		return EmbeddingConfig{Endpoint: "https://api.siliconflow.cn/v1/embeddings", Model: "BAAI/bge-m3"}, nil
	}
	if err != nil {
		return EmbeddingConfig{}, err
	}
	c.HasKey = c.APIKey != ""
	return c, nil
}

// SaveEmbeddingConfig preserves a key only for the same endpoint, so changing
// services cannot accidentally disclose an existing credential to a new host.
func (s *Store) SaveEmbeddingConfig(ctx context.Context, c EmbeddingConfig) error {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Model = strings.TrimSpace(c.Model)
	c.APIKey = strings.TrimSpace(c.APIKey)
	if c.ClearKey {
		if c.Enabled {
			return fmt.Errorf("disable embeddings before clearing the API key")
		}
		c.APIKey = ""
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("embedding endpoint must be an HTTPS URL without credentials or query")
	}
	if c.Model == "" || len(c.Model) > 200 || len(c.APIKey) > 4096 || strings.ContainsAny(c.APIKey, "\r\n\x00") {
		return fmt.Errorf("invalid embedding model or key")
	}
	db, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var oldEndpoint, oldModel, oldKey string
	err = tx.QueryRowContext(ctx, `SELECT endpoint,model,api_key FROM knowledge_embedding_config WHERE id=1`).Scan(&oldEndpoint, &oldModel, &oldKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if c.APIKey == "" && !c.ClearKey && c.Endpoint == oldEndpoint {
		c.APIKey = oldKey
	}
	if c.Enabled && c.APIKey == "" {
		return fmt.Errorf("an API key is required to enable embeddings")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO knowledge_embedding_config(id,enabled,endpoint,model,api_key) VALUES(1,?,?,?,?) ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,endpoint=excluded.endpoint,model=excluded.model,api_key=excluded.api_key`, c.Enabled, c.Endpoint, c.Model, c.APIKey)
	if err != nil {
		return err
	}
	if !c.Enabled || c.Endpoint != oldEndpoint || c.Model != oldModel {
		if _, err = tx.ExecContext(ctx, `DELETE FROM knowledge_embedding_vectors`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
