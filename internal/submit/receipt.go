package submit

import (
	"fmt"
	"strings"
	"time"
)

// DetailsKind marks a successful tool result that carries a receipt, so a
// channel can render it without parsing the model's retelling.
const DetailsKind = "task_receipt"

// receipt is the structured result of a submit. It travels in the tool
// result's Details for channels, and as markdown for the model and the user.
type receipt struct {
	Summary   string       `json:"summary,omitempty"`
	Branch    string       `json:"branch"`
	Commit    string       `json:"commit,omitempty"`
	Pushed    bool         `json:"pushed"`
	Files     []fileChange `json:"files,omitempty"`
	Gate      []gateStep   `json:"gate,omitempty"`
	MRURL     string       `json:"mr_url,omitempty"`
	Preview   []string     `json:"preview,omitempty"`
	Ready     bool         `json:"ready,omitempty"` // new_task: the workspace is back on the base branch
	Warnings  []string     `json:"warnings,omitempty"`
	NextSteps string       `json:"next_steps,omitempty"`
}

type fileChange struct {
	Status string `json:"status"` // A, M or D
	Path   string `json:"path"`
}

type gateStep struct {
	Command    string `json:"command"`
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output,omitempty"` // the failing step
}

// markdown renders the receipt the way the user reads it: what changed, how
// the checks went, where to look, and what happens next.
func (r *receipt) markdown() string {
	var b strings.Builder
	if r.Ready {
		fmt.Fprintf(&b, "✅ 已回到主分支 `%s`，可以开始新任务。\n\n", r.Branch)
		writeWarnings(&b, r.Warnings)
		b.WriteString(r.NextSteps + "\n")
		return b.String()
	}
	fmt.Fprintf(&b, "### 已提交：%s\n\n", r.Summary)
	fmt.Fprintf(&b, "**分支** `%s`", r.Branch)
	if r.Commit != "" {
		fmt.Fprintf(&b, " ｜ **提交** `%s`", r.Commit)
	}
	if r.Pushed {
		b.WriteString(" ｜ 已推送到远端")
	} else {
		b.WriteString(" ｜ 仅本地提交，未推送")
	}
	b.WriteString("\n\n")
	if len(r.Files) > 0 {
		fmt.Fprintf(&b, "**改了什么**（%d 个文件）\n", len(r.Files))
		for _, f := range r.Files {
			fmt.Fprintf(&b, "- %s `%s`\n", changeLabel(f.Status), f.Path)
		}
		b.WriteString("\n")
	}
	if len(r.Gate) > 0 {
		b.WriteString("**质检结果**\n")
		for _, step := range r.Gate {
			mark := "✔"
			if !step.OK {
				mark = "✘"
			}
			fmt.Fprintf(&b, "- %s %s（%s）\n", mark, step.Command, formatDuration(step.DurationMS))
		}
		b.WriteString("\n")
	}
	if len(r.Preview) > 0 {
		b.WriteString("**测试环境预览**\n")
		for _, link := range r.Preview {
			fmt.Fprintf(&b, "- [打开 %s](%s)\n", previewLabel(link), link)
		}
		b.WriteString("\n")
	}
	if r.MRURL != "" {
		fmt.Fprintf(&b, "**合并请求**：[查看](%s)\n\n", r.MRURL)
	}
	writeWarnings(&b, r.Warnings)
	if r.NextSteps != "" {
		b.WriteString("**下一步**：" + r.NextSteps + "\n")
	}
	return b.String()
}

func writeWarnings(b *strings.Builder, warnings []string) {
	for _, warning := range warnings {
		b.WriteString("⚠️ " + warning + "\n")
	}
	if len(warnings) > 0 {
		b.WriteString("\n")
	}
}

func changeLabel(status string) string {
	switch status {
	case "A":
		return "新增"
	case "M":
		return "修改"
	case "D":
		return "删除"
	}
	return status
}

// previewLabel shows the path of a preview link instead of the whole URL.
func previewLabel(link string) string {
	if cut := strings.LastIndex(link, "://"); cut >= 0 {
		if slash := strings.Index(link[cut+3:], "/"); slash >= 0 {
			return link[cut+3+slash:]
		}
	}
	return link
}

func formatDuration(ms int64) string {
	if ms < 1000 {
		return "不到 1 秒"
	}
	seconds := int(time.Duration(ms) * time.Millisecond / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%d 秒", seconds)
	}
	return fmt.Sprintf("%d 分 %d 秒", seconds/60, seconds%60)
}

// tail keeps the end of a command's output, where the cause usually is.
func tail(output string) string {
	output = strings.TrimRight(output, "\n")
	const limit = 1500
	if runes := []rune(output); len(runes) > limit {
		return "…\n" + string(runes[len(runes)-limit:])
	}
	return output
}

func listPaths(paths []string) string {
	var b strings.Builder
	for i, path := range paths {
		if i == 10 {
			fmt.Fprintf(&b, "- …等 %d 处", len(paths))
			break
		}
		b.WriteString("- " + path + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstLine(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	const limit = 300
	if runes := []rune(line); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return line
}
