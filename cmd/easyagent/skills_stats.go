package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nightgopher/easyagent/sdk/config"
	"github.com/nightgopher/easyagent/sdk/runtime"
	"github.com/nightgopher/easyagent/sdk/skill"
)

// runSkillsStatsCLI implements `easyagent skills-stats [--json]`:
// 汇总 usage.jsonl（技能读取埋点），对照已安装技能给出使用排行与从未使用清单。
func runSkillsStatsCLI(args []string) error {
	fs := flag.NewFlagSet("skills-stats", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	if err := fs.Parse(args); err != nil {
		return err
	}

	home := config.HomeDir()
	usagePath := filepath.Join(home, "usage.jsonl")
	counts, last, err := skill.AggregateUsage(usagePath)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", usagePath, err)
	}

	// 已安装技能：与 会话加载逻辑一致（项目目录优先，其次个人目录）。
	cwd, _ := os.Getwd()
	installed := skill.LoadFromDirs(runtime.DefaultSkillDirs(cwd)...).Skills

	type row struct {
		Name     string `json:"name"`
		Count    int    `json:"count"`
		LastUsed string `json:"last_used,omitempty"`
	}
	neverUsed := []string{}
	byName := map[string]bool{}
	for _, s := range installed {
		byName[s.Name] = true
	}
	rows := []row{}
	for name, count := range counts {
		r := row{Name: name, Count: count}
		if ts := last[name]; ts > 0 {
			r.LastUsed = time.UnixMilli(ts).Format("2006-01-02 15:04")
		}
		rows = append(rows, r)
	}
	for _, s := range installed {
		if _, used := counts[s.Name]; !used {
			neverUsed = append(neverUsed, s.Name)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Count > rows[j].Count })
	sort.Strings(neverUsed)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"usage_file":  usagePath,
			"total_reads": len(counts),
			"ranking":     rows,
			"never_used":  neverUsed,
		})
	}

	fmt.Printf("技能使用统计（数据源 %s）\n", usagePath)
	fmt.Printf("已安装 %d 个技能，其中 %d 个有读取记录\n\n", len(installed), len(rows))
	if len(rows) > 0 {
		fmt.Println("使用排行:")
		for i, r := range rows {
			fmt.Printf("  %2d. %-30s ×%-4d 最近 %s\n", i+1, r.Name, r.Count, r.LastUsed)
		}
		fmt.Println()
	}
	if len(neverUsed) > 0 {
		fmt.Printf("从未读取 (%d 个):\n", len(neverUsed))
		for _, n := range neverUsed {
			fmt.Printf("  - %s\n", n)
		}
	} else if len(installed) > 0 {
		fmt.Println("所有已安装技能都至少被读取过一次。")
	}
	return nil
}
