package agentdata

import (
	"context"
	"testing"
)

func TestEmbeddingConfigCredentialIsolation(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir())
	initial, err := s.EmbeddingConfig(ctx)
	if err != nil || initial.Enabled || initial.HasKey {
		t.Fatalf("unexpected default: %+v %v", initial, err)
	}
	initial.Enabled = true
	initial.APIKey = "secret"
	if err = s.SaveEmbeddingConfig(ctx, initial); err != nil {
		t.Fatal(err)
	}
	saved, err := s.EmbeddingConfig(ctx)
	if err != nil || saved.APIKey != "secret" || !saved.HasKey {
		t.Fatalf("config not saved: %v", err)
	}
	saved.APIKey = ""
	saved.Model = "other"
	if err = s.SaveEmbeddingConfig(ctx, saved); err != nil {
		t.Fatal(err)
	}
	saved.Endpoint = "https://other.example/v1/embeddings"
	if err = s.SaveEmbeddingConfig(ctx, saved); err == nil {
		t.Fatal("enabled new endpoint reused existing key")
	}
	saved.Enabled = false
	if err = s.SaveEmbeddingConfig(ctx, saved); err != nil {
		t.Fatal(err)
	}
	saved.ClearKey = true
	saved.APIKey = "must-not-be-saved"
	if err = s.SaveEmbeddingConfig(ctx, saved); err != nil {
		t.Fatal(err)
	}
	saved, err = s.EmbeddingConfig(ctx)
	if err != nil || saved.APIKey != "" || saved.HasKey {
		t.Fatalf("key leaked to new endpoint: %v", err)
	}
}
