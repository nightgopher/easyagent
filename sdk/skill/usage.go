package skill

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// UsageEntry 是技能使用埋点的一行记录。
type UsageEntry struct {
	// Time 毫秒时间戳。
	Time int64 `json:"ts"`
	// Event 固定为 "skill_read"（模型通过 read 工具读取技能文件）。
	Event string `json:"event"`
	// Skill 命中的技能名；无法归属时为路径中推导的目录名，再退化为空。
	Skill string `json:"skill"`
	// Path 被读取的文件路径。
	Path string `json:"path"`
	// Session 产生读取的会话 ID，可能为空。
	Session string `json:"session,omitempty"`
}

// UsageRecorder 记录技能文件被读取的事件，写入 JSONL（DataDir/usage.jsonl）。
// 埋点绝不影响主流程：任何写入失败都静默忽略。
type UsageRecorder struct {
	mu      sync.Mutex
	logPath string
	session string
	// fileByPath 精确匹配：SKILL.md 绝对路径 -> 技能名。
	fileByPath map[string]string
	// dirByName 前缀匹配：技能目录 -> 技能名。
	dirByName map[string]string
}

// NewUsageRecorder 构造记录器；skills 为该会话加载的技能清单。
// logPath 为空时返回 nil（调用方无需判空以外再做开关）。
func NewUsageRecorder(logPath, session string, skills []Skill) *UsageRecorder {
	if logPath == "" {
		return nil
	}
	r := &UsageRecorder{
		logPath:    logPath,
		session:    session,
		fileByPath: map[string]string{},
		dirByName:  map[string]string{},
	}
	for _, s := range skills {
		if s.FilePath != "" {
			r.fileByPath[filepath.Clean(s.FilePath)] = s.Name
		}
		if s.BaseDir != "" {
			r.dirByName[filepath.Clean(s.BaseDir)] = s.Name
		}
	}
	return r
}

// RecordSkillRead 在 read 工具成功读取路径后调用；路径不属于任何技能时忽略。
func (r *UsageRecorder) RecordSkillRead(path string) {
	if r == nil || path == "" {
		return
	}
	clean := filepath.Clean(path)
	name, ok := r.fileByPath[clean]
	if !ok {
		for dir, n := range r.dirByName {
			if withinDir(dir, clean) {
				name = n
				ok = true
				break
			}
		}
	}
	if !ok {
		return
	}
	entry := UsageEntry{Time: time.Now().UnixMilli(), Event: "skill_read", Skill: name, Path: clean, Session: r.session}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.OpenFile(r.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

// withinDir 判断 path 是否位于 dir 之内（含 dir 自身）。
func withinDir(dir, path string) bool {
	if dir == path {
		return true
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// AggregateUsage 汇总 usage.jsonl：技能调用次数与最近一次时间。
// 文件不存在时返回空结果而非错误（尚无数据是常态）。
func AggregateUsage(logPath string) (map[string]int, map[string]int64, error) {
	counts := map[string]int{}
	last := map[string]int64{}
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return counts, last, nil
		}
		return nil, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e UsageEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || e.Event != "skill_read" {
			continue
		}
		counts[e.Skill]++
		if e.Time > last[e.Skill] {
			last[e.Skill] = e.Time
		}
	}
	return counts, last, sc.Err()
}
