package larkwatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverSessionIDSingleOwner(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "feishu-routes.json")
	if err := os.WriteFile(p, []byte(`{"ou_a":"sess_1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := DiscoverSessionID(p)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if id != "sess_1" {
		t.Errorf("session = %q, want sess_1", id)
	}
}

func TestDiscoverSessionIDObjectValues(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "feishu-routes.json")
	// 桥接实际写入的形态：值是 {"session_id": ...} 对象。
	if err := os.WriteFile(p, []byte(`{"ou_a":{"session_id":"sess_obj"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := DiscoverSessionID(p)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if id != "sess_obj" {
		t.Errorf("session = %q, want sess_obj", id)
	}
}

func TestDiscoverSessionIDMultipleCandidates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "feishu-routes.json")
	if err := os.WriteFile(p, []byte(`{"ou_a":"sess_1","ou_b":"sess_2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := DiscoverSessionID(p)
	if err == nil || !strings.Contains(err.Error(), "multiple sessions") {
		t.Fatalf("want multiple-session error, got %v", err)
	}
}

func TestDiscoverSessionIDMissingAndBroken(t *testing.T) {
	if id, err := DiscoverSessionID(filepath.Join(t.TempDir(), "absent.json")); err == nil || id != "" {
		t.Fatalf("missing file should error, got id=%q err=%v", id, err)
	}
	p := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverSessionID(p); err == nil {
		t.Fatal("broken json should error")
	}
}

func TestDiscoverSessionIDSkipsEmptyAndFallsThrough(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(empty, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(t.TempDir(), "routes.json")
	if err := os.WriteFile(good, []byte(`{"ou_a":"sess_9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := DiscoverSessionID(empty, good)
	if err != nil || id != "sess_9" {
		t.Fatalf("id=%q err=%v, want sess_9", id, err)
	}
}
