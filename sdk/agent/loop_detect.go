package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// LoopDetectSettings 控制循环检测行为（参考 compaction.Settings 的 Settings 惯例）。
//
// 检测目标：Agent 连续重复调用"相同工具 + 相同参数"（典型空转：反复 read 同一文件、
// 反复 grep 同一 pattern）。检测到后在下一次请求前注入柔性提醒，不强制中断。
type LoopDetectSettings struct {
	Enabled          bool   // 是否启用循环检测
	Threshold        int    // 连续重复多少次触发（默认 5）
	ReminderTemplate string // 注入给 LLM 的提醒模板，第一个 %s=工具名，第二个 %s=参数摘要，%d=重复次数
}

// DefaultLoopDetectSettings 返回保守的默认配置。
func DefaultLoopDetectSettings() LoopDetectSettings {
	return LoopDetectSettings{
		Enabled:   true,
		Threshold: 5,
		ReminderTemplate: "You have called tool %q with the same arguments %d times in a row — " +
			"this looks like a loop. Step back, reconsider your approach, and try a different method. " +
			"(repeated args summary: %s)",
	}
}

// loopDetector 跟踪连续相同的 tool call，per-prompt 生命周期。
// 指纹用 SHA256(name + ":" + args)，与 DeepV loopDetectionService.getToolCallKey 一致。
type loopDetector struct {
	lastFingerprint string // 上一个 tool call 的指纹；空串表示尚无记录
	repeatCount     int    // 当前指纹的连续重复次数
	readOnlyCalls   int
	recentOverlaps  []bool
	recentReads     []readSpan
}

type readSpan struct {
	path  string
	pages string
	start int
	end   int
}

const explorationThreshold = 12

// observeExploration identifies repeated, overlapping reads interspersed with
// other read-only tools. Distinct file regions and ordinary investigations do
// not trigger it, and any other tool resets the observation window.
func (d *loopDetector) observeExploration(name, args string) bool {
	switch name {
	case "read", "grep", "find", "ls":
		d.readOnlyCalls++
	default:
		d.readOnlyCalls, d.recentOverlaps, d.recentReads = 0, nil, nil
		return false
	}
	overlaps := false
	if name == "read" {
		var p struct {
			Path   string `json:"path"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
			Pages  string `json:"pages"`
		}
		if json.Unmarshal([]byte(args), &p) == nil && p.Path != "" {
			start := max(0, p.Offset)
			limit := p.Limit
			if limit <= 0 {
				limit = 2000 // read tool default
			}
			end := start + limit
			if end < start { // guard overflowing untrusted arguments
				end = int(^uint(0) >> 1)
			}
			current := readSpan{path: filepath.Clean(p.Path), pages: p.Pages, start: start, end: end}
			for _, old := range d.recentReads {
				if old.path == current.path && old.pages == current.pages && old.start < current.end && current.start < old.end {
					overlaps = true
					break
				}
			}
			d.recentReads = append(d.recentReads, current)
			if len(d.recentReads) > 24 {
				d.recentReads = d.recentReads[1:]
			}
		}
	}
	d.recentOverlaps = append(d.recentOverlaps, overlaps)
	if len(d.recentOverlaps) > explorationThreshold {
		d.recentOverlaps = d.recentOverlaps[1:]
	}
	overlapCount := 0
	for _, overlap := range d.recentOverlaps {
		if overlap {
			overlapCount++
		}
	}
	return d.readOnlyCalls >= explorationThreshold && d.readOnlyCalls%explorationThreshold == 0 && overlapCount >= 3
}

// observe 记录一次 tool call，返回是否检测到循环（连续重复 >= Threshold）。
// name/args 来自 ai.ToolCall。指纹含 args，故同工具不同参数不算重复。
func (d *loopDetector) observe(threshold int, name, args string) bool {
	fp := fingerprint(name, args)
	if fp == d.lastFingerprint {
		d.repeatCount++
	} else {
		d.lastFingerprint = fp
		d.repeatCount = 1
	}
	return d.repeatCount >= threshold
}

// reset 清空状态，用于每次新 prompt 开始时（per-prompt 计数，不跨对话累积）。
func (d *loopDetector) reset() {
	d.lastFingerprint = ""
	d.repeatCount = 0
	d.readOnlyCalls = 0
	d.recentOverlaps = nil
	d.recentReads = nil
}

// fingerprint 计算 tool call 的指纹：SHA256(name + ":" + args)。
func fingerprint(name, args string) string {
	h := sha256.Sum256([]byte(name + ":" + args))
	return hex.EncodeToString(h[:])
}

// summarizeArgs 生成参数的人类可读摘要，用于提醒文案。截断避免过长。
func summarizeArgs(args string) string {
	s := strings.TrimSpace(args)
	if len(s) > 80 {
		return s[:80] + "..."
	}
	if s == "" {
		return "(none)"
	}
	return s
}

// renderReminder 用 ReminderTemplate 渲染提醒文案。
func renderReminder(tmpl, toolName, args string, count int) string {
	return fmt.Sprintf(tmpl, toolName, count, summarizeArgs(args))
}
