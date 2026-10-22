package main

import (
	"image"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// A long code line scrolls inside its block; the block stays within the
// reading column instead of stretching to the window edge (#258).
func TestCodeBlockStaysInReadingColumn(t *testing.T) {
	a := alignmentPreview(t)
	long := "mkdir -p ~/.ssh && echo 'ssh-rsa " + strings.Repeat("AAAAB3NzaC1yc2E", 40) + "' >> ~/.ssh/authorized_keys"
	a.current().Messages = []*message{{User: true, Text: "怎么配置？"}, {Text: "执行：\n\n```bash\n" + long + "\n```\n\n完成。"}}
	tt := ui.NewTester(a.View, 1240, 820)
	copyButton, ok := tt.Find("复制代码")
	if !ok {
		t.Fatal("missing code copy button")
	}
	input, ok := tt.Find("输入面板")
	if !ok {
		t.Fatal("missing composer")
	}
	if copyButton.X+copyButton.W > input.X+input.W+1 {
		t.Fatalf("code block overflows the reading column: copy=%+v composer=%+v", copyButton, input)
	}
}

// Copying code shows a check for two seconds and copies without the fence newline (#259).
func TestCodeCopyShowsConfirmation(t *testing.T) {
	a := alignmentPreview(t)
	a.current().Messages = []*message{{User: true, Text: "代码"}, {Text: "```go\nfmt.Println(1)\n```"}}
	tt := ui.NewTester(a.View, 1240, 820)
	click(t, tt, "复制代码")
	tt.Frame()
	if tt.Clipboard() != "fmt.Println(1)" {
		t.Fatalf("clipboard %q", tt.Clipboard())
	}
	if _, ok := tt.Find("已复制"); !ok {
		t.Fatal("copy gave no feedback")
	}
}

// History keeps message actions hidden; the latest reply shows them, and the
// duplicate tool-group fork button is gone (#257, #265).
func TestMessageActionsRevealOnlyWhereNeeded(t *testing.T) {
	a := alignmentPreview(t)
	a.current().Messages = []*message{
		{User: true, Text: "第一问"}, {Text: "第一答"},
		{Kind: "tool", Tool: "read", Status: "完成"},
		{User: true, Text: "第二问"}, {Text: "第二答"},
	}
	tt := ui.NewTester(a.View, 1240, 820)
	if _, ok := tt.Find("从工具结果创建分支"); ok {
		t.Fatal("tool group still has its own fork button")
	}
	img := tt.Image()
	latest, ok := tt.Find("从回复 4 创建分支")
	if !ok {
		t.Fatal("latest reply lost its fork action")
	}
	history, ok := tt.Find("从回复 1 创建分支")
	if !ok {
		t.Fatal("history fork must stay reachable by hover or keyboard")
	}
	if !hasInk(img, latest) || hasInk(img, history) {
		t.Fatalf("visibility wrong: latest drawn=%v history drawn=%v", hasInk(img, latest), hasInk(img, history))
	}
}

// hasInk reports whether anything visible is drawn inside r: an empty,
// transparent area has a flat color, an icon has strokes of another shade.
func hasInk(img image.Image, r ui.Rect) bool {
	lo, hi := uint32(1<<32-1), uint32(0)
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			l := (cr + cg + cb) / 3
			lo, hi = min(lo, l), max(hi, l)
		}
	}
	return hi-lo > 0x1800
}
