package skill

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUsageRecorderRecordsSkillReads(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("---\nname: demo\ndescription: d\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "usage.jsonl")
	rec := NewUsageRecorder(logPath, "sess-1", []Skill{{
		Name:     "demo",
		FilePath: skillFile,
		BaseDir:  skillDir,
	}})

	// 命中：精确 SKILL.md、目录内 references、大小写不变的路径规范化
	rec.RecordSkillRead(skillFile)
	rec.RecordSkillRead(filepath.Join(skillDir, "references", "guide.md"))
	// 不命中：无关文件、技能目录之外
	rec.RecordSkillRead(filepath.Join(dir, "main.go"))
	rec.RecordSkillRead(filepath.Join(dir, "other-skill", "SKILL.md"))

	lines := readLines(t, logPath)
	if len(lines) != 2 {
		t.Fatalf("期望记录 2 条，实际 %d: %v", len(lines), lines)
	}
	if lines[0].Skill != "demo" || lines[0].Event != "skill_read" || lines[0].Session != "sess-1" {
		t.Fatalf("第一条记录字段不符: %+v", lines[0])
	}

	counts, last, err := AggregateUsage(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if counts["demo"] != 2 {
		t.Fatalf("聚合计数期望 2，实际 %d", counts["demo"])
	}
	if last["demo"] == 0 {
		t.Fatal("聚合最近时间缺失")
	}
}

func TestUsageRecorderToleratesErrors(t *testing.T) {
	skillDir := t.TempDir()
	rec := NewUsageRecorder(
		filepath.Join(t.TempDir(), "no-such-dir", "usage.jsonl"), // 日志目录不存在
		"s",
		[]Skill{{Name: "demo", FilePath: filepath.Join(skillDir, "SKILL.md"), BaseDir: skillDir}},
	)
	// 写入失败必须静默：不 panic、不外泄错误（埋点绝不影响主流程）
	rec.RecordSkillRead(filepath.Join(skillDir, "SKILL.md"))

	// nil 接收者安全
	var nilRec *UsageRecorder
	nilRec.RecordSkillRead("/tmp/x")
}

func readLines(t *testing.T, path string) []UsageEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []UsageEntry
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var e UsageEntry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("非法 JSONL 行 %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
