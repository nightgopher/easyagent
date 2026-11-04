package handoff

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSaveAndLoad 保护 TASK.md 的磁盘格式契约：写入后必须能原样回读，
// 格式破坏会直接丢失用户的手动交接文档。
func TestSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()

	doc := NewTaskDocument("Test task goal")
	doc.Progress = []string{"Step 1 done", "Step 2 done"}
	doc.NextSteps = []string{"Step 3", "Step 4"}
	doc.FilesChanged = []string{"file1.go", "file2.go"}

	if err := Save(tmpDir, doc); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil after Save")
	}
	if loaded.Goal != doc.Goal || loaded.State != doc.State {
		t.Fatalf("round-trip mismatch: %+v vs %+v", loaded, doc)
	}
	if len(loaded.Progress) != 2 || len(loaded.NextSteps) != 2 {
		t.Fatalf("list fields lost: %+v", loaded)
	}
}

// TestParseTaskDocument 保护 TASK.md 的 Markdown 解析契约：
// 用户手写或旧版本生成的 TASK.md 必须能被解析（向前兼容面）。
func TestParseTaskDocument(t *testing.T) {
	content := `# 📋 Task Handoff

**Goal:** Implement feature X

**State:** in-progress

**Created:** 2026-07-05 08:00:00
**Updated:** 2026-07-05 09:00:00

## ✅ Progress

- Created files
- Wrote tests

## ➡️ Next Steps

- [ ] Deploy
- [ ] Write docs

## 📁 Files Changed

- ` + "`main.go`" + `
- ` + "`test.go`" + `

## 📝 Notes

Important context here.
`

	doc := parseTaskDocument(content)

	if doc.Goal != "Implement feature X" {
		t.Errorf("Goal = %q, want %q", doc.Goal, "Implement feature X")
	}
	if doc.State != StateInProgress {
		t.Errorf("State = %q, want %q", doc.State, StateInProgress)
	}
	if len(doc.Progress) != 2 {
		t.Errorf("Progress count = %d, want 2", len(doc.Progress))
	}
	if len(doc.NextSteps) != 2 {
		t.Errorf("NextSteps count = %d, want 2", len(doc.NextSteps))
	}
	if doc.NextSteps[0] != "Deploy" {
		t.Errorf("NextSteps[0] = %q, want %q", doc.NextSteps[0], "Deploy")
	}
	if len(doc.FilesChanged) != 2 {
		t.Errorf("FilesChanged count = %d, want 2", len(doc.FilesChanged))
	}
	if doc.Notes != "Important context here." {
		t.Errorf("Notes = %q", doc.Notes)
	}
}

// TestSaveWritesFile 验证 Save 确实落盘（而非仅内存）。
func TestSaveWritesFile(t *testing.T) {
	tmpDir := t.TempDir()
	doc := NewTaskDocument("persist check")
	require.NoError(t, Save(tmpDir, doc))

	path := filepath.Join(tmpDir, TaskDir, TaskFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("TASK.md not written: %v", err)
	}
}
