package skillmarket

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// buildPackageZip writes the given entries into a zip file and returns its
// path. A name ending in "/" creates a directory entry; empty content is
// fine for those.
func buildPackageZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		file, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "package.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// extractFixture unpacks a fixture zip into a fresh stage and runs the
// install-path normalization over it, returning the stage directory.
func extractFixture(t *testing.T, entries map[string]string) string {
	t.Helper()
	stage := t.TempDir()
	if err := extractZip(buildPackageZip(t, entries), stage); err != nil {
		t.Fatal(err)
	}
	if err := normalizeExtractedLayout(stage); err != nil {
		t.Fatal(err)
	}
	return stage
}

func requireFiles(t *testing.T, stage string, want ...string) {
	t.Helper()
	for _, rel := range want {
		if _, err := os.Stat(filepath.Join(stage, rel)); err != nil {
			t.Fatalf("expected %s after normalization: %v", rel, err)
		}
	}
}

func forbidPaths(t *testing.T, stage string, unwanted ...string) {
	t.Helper()
	for _, rel := range unwanted {
		if _, err := os.Stat(filepath.Join(stage, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s should not exist after normalization", rel)
		}
	}
}

const fixtureSkillMD = "---\nname: demo-skill\ndescription: demo\n---\n\n# Demo\n"

func TestNormalizeFlattensSingleWrappedDir(t *testing.T) {
	stage := extractFixture(t, map[string]string{
		"demo-skill/SKILL.md":      fixtureSkillMD,
		"demo-skill/scripts/go.py": "#!/usr/bin/env python3\n",
	})
	requireFiles(t, stage, "SKILL.md", "scripts/go.py")
	forbidPaths(t, stage, "demo-skill")
}

func TestNormalizeFlattensDoubleWrappedDir(t *testing.T) {
	// Mirrors the real sanwan-whiteboard-ppt package: the zip held
	// <name>/<name>/SKILL.md, which used to install with two extra levels.
	stage := extractFixture(t, map[string]string{
		"demo-skill/demo-skill/SKILL.md":              fixtureSkillMD,
		"demo-skill/demo-skill/scripts/build_pptx.py": "#!/usr/bin/env python3\n",
	})
	requireFiles(t, stage, "SKILL.md", "scripts/build_pptx.py")
	forbidPaths(t, stage, "demo-skill")
}

func TestNormalizeKeepsFlatPackage(t *testing.T) {
	stage := extractFixture(t, map[string]string{
		"SKILL.md":         fixtureSkillMD,
		"scripts/run.sh":   "#!/bin/sh\n",
		"references/notes": "notes",
	})
	requireFiles(t, stage, "SKILL.md", "scripts/run.sh", "references/notes")
}

func TestNormalizeKeepsAmbiguousLayout(t *testing.T) {
	// A root-level file next to the wrapped skill makes the lift ambiguous;
	// the layout must be published exactly as extracted.
	stage := extractFixture(t, map[string]string{
		"README.md":           "# notes\n",
		"demo-skill/SKILL.md": fixtureSkillMD,
	})
	requireFiles(t, stage, "README.md", "demo-skill/SKILL.md")
}

func TestNormalizeToleratesMacOSZipJunk(t *testing.T) {
	stage := extractFixture(t, map[string]string{
		"__MACOSX/":            "",
		"demo-skill/.DS_Store": "",
		"demo-skill/SKILL.md":  fixtureSkillMD,
	})
	requireFiles(t, stage, "SKILL.md")
	forbidPaths(t, stage, "__MACOSX", "demo-skill", ".DS_Store")
}

func TestNormalizeNoopOnEmptyStage(t *testing.T) {
	stage := t.TempDir()
	if err := normalizeExtractedLayout(stage); err != nil {
		t.Fatalf("normalize on empty stage: %v", err)
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected untouched empty stage, got %v", entries)
	}
}
