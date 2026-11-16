package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newTestSendFile(t *testing.T, workspace string) *SendFileTool {
	t.Helper()
	return NewSendFileTool(
		WithSendFileWorkspace(workspace),
		WithSendFilePathPolicy(PathPolicy{AllowOutsideWorkspace: false}),
	)
}

func marshalParams(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSendFileDeliversWorkspaceFile(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "report.md"), []byte("# hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := newTestSendFile(t, ws).Execute(context.Background(),
		marshalParams(t, SendFileParams{Path: "report.md", Caption: "周报"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("unexpected error result:", result.Content)
	}
	details, ok := result.Details.(map[string]any)
	if !ok || details["kind"] != "file" || details["path"] != "report.md" || details["name"] != "report.md" {
		t.Fatalf("details wrong: %+v", result.Details)
	}
	if details["size"].(int64) != 7 {
		t.Fatalf("size wrong: %v", details["size"])
	}
	if result.UserFacing == "" || result.Content == "" {
		t.Fatal("UserFacing and Content must both be set")
	}
}

func TestSendFileRejectsOutsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := newTestSendFile(t, ws).Execute(context.Background(),
		marshalParams(t, SendFileParams{Path: target}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("outside-workspace path must be rejected")
	}
}

func TestSendFileRejectsMissingAndDirectories(t *testing.T) {
	ws := t.TempDir()
	tool := newTestSendFile(t, ws)
	for _, tc := range []struct {
		name string
		path string
	}{
		{"missing file", "nope.txt"},
		{"directory", "."},
	} {
		result, err := tool.Execute(context.Background(), marshalParams(t, SendFileParams{Path: tc.path}), nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !result.IsError {
			t.Fatalf("%s must be rejected", tc.name)
		}
	}
}

func TestSendFileAllowOutsideWorkspaceOption(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "ok.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewSendFileTool(WithSendFileWorkspace(ws), WithSendFilePathPolicy(PathPolicy{AllowOutsideWorkspace: true}))
	result, err := tool.Execute(context.Background(), marshalParams(t, SendFileParams{Path: target}), nil)
	if err != nil || result.IsError {
		t.Fatalf("allow-outside policy must permit the file: %v %v", err, result.Content)
	}
}

func TestSendFileValidateRejectsEmptyPath(t *testing.T) {
	tool := NewSendFileTool()
	if _, err := tool.Validate(marshalParams(t, SendFileParams{Path: " "})); err == nil {
		t.Fatal("empty path must fail validation")
	}
	if _, err := tool.Validate(marshalParams(t, SendFileParams{Path: "a.txt"})); err != nil {
		t.Fatal(err)
	}
}
