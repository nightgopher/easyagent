package models

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "models.json")

	jsonContent := `[
		{"id": "custom-model-1", "provider": "openai", "name": "Custom 1", "context_window": 64000},
		{"id": "custom-model-2", "provider": "anthropic", "name": "Custom 2", "context_window": 200000}
	]`
	if err := os.WriteFile(path, []byte(jsonContent), 0o644); err != nil {
		t.Fatalf("Write test file: %v", err)
	}

	r := NewRegistry()
	if err := r.LoadFromFile(path); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	m, ok := r.Get("custom-model-1")
	if !ok {
		t.Fatal("Expected to find custom-model-1")
	}
	if m.ContextWindow != 64000 {
		t.Errorf("ContextWindow = %d, want 64000", m.ContextWindow)
	}

	m2, ok := r.Get("custom-model-2")
	if !ok {
		t.Fatal("Expected to find custom-model-2")
	}
	if m2.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", m2.Provider)
	}
}

func TestLoadFromFileSkipsInvalid(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "models.json")

	// Entries with empty ID or provider should be skipped
	jsonContent := `[
		{"id": "", "provider": "openai", "name": "Invalid 1"},
		{"id": "valid", "provider": "", "name": "Invalid 2"},
		{"id": "good", "provider": "openai", "name": "Good"}
	]`
	require.NoError(t, os.WriteFile(path, []byte(jsonContent), 0o644))

	r := NewRegistry()
	require.NoError(t, r.LoadFromFile(path))

	if _, ok := r.Get("good"); !ok {
		t.Error("Expected 'good' model to be registered")
	}
	if _, ok := r.Get("valid"); ok {
		t.Error("Expected 'valid' to be skipped (empty provider)")
	}
}
