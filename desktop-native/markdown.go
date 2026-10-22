package main

import (
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"
)

// MyGo keeps the parsed document, selection and syntax tokens on the view.
// Image loading uses the active core and its workspace access checks.
type markdownDocument struct {
	MediaScope string
	Anchor     string
	options    markdown.Options
	ready      bool
	source     *editor.State
}

func (d *markdownDocument) render(c *ui.Context, reply string, streaming ...bool) {
	// Markdown expands every table cell/block into layout nodes. Rendering a
	// large document every frame can evict CoreText's layout cache and starve
	// input. Bound rich layout work; the editor virtualizes the complete source.
	if largeRichText(reply) {
		ui.Text(c, "内容较长，已切换到可滚动源码视图；完整内容可选择、搜索和复制。").FontSize(12).TextColor(c.Theme().TextMuted)
		d.source = nativeSource(c, reply, 400, "大文本源码")
		return
	}
	if !d.ready {
		d.ready = true
		d.options.Code = map[string]func(*ui.Context, *markdown.CodeBlockView){"mermaid": nativeDiagram}
		d.options.Reveal = &markdown.Reveal{Unit: markdown.AtOnce, Fade: -1}
		d.options.CodeBlock = func(c *ui.Context, b *markdown.CodeBlockView) {
			// Stretch to the text column and clip: long lines scroll inside the
			// block instead of widening it past the reply.
			ui.Column(c).Gap(4).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(6).AlignSelf(ui.Stretch).MinWidth(0).Clip().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Padding(4, 6, 0, 12).Children(func() {
					ui.Text(c, b.Language).FontSize(12).TextColor(c.Theme().TextMuted).Unselectable()
					ui.Spacer(c)
					copyCodeButton(c, strings.TrimSuffix(b.Code(), "\n"), b.Open)
				})
				b.Body(c).MinWidth(0)
			})
		}
	}
	d.options.Theme = &markdown.Theme{FontSize: 14, LineHeight: 1.75, BlockSpacing: .85}
	if len(streaming) > 0 && streaming[0] {
		markdown.Stream(c, reply, &d.options).MinWidth(0)
	} else {
		markdown.View(c, reply, &d.options).MinWidth(0)
	}
}

// copyCodeButton is a small icon button that shows a check for two seconds
// after copying, so repeated clicks are not needed to confirm it worked.
func copyCodeButton(c *ui.Context, code string, open bool) ui.Element {
	btn := ui.ButtonBase(c).Label("复制代码").Size(26, 26).Center().Radius(5).TextColor(c.Theme().TextMuted).Disabled(open)
	copiedAt := ui.Local(btn, "copied", func() time.Time { return time.Time{} })
	copied := !copiedAt.IsZero() && c.Now().Sub(*copiedAt) < 2*time.Second
	if btn.Hovered() && !open {
		btn.Background(c.Theme().SurfaceHover).TextColor(c.Theme().Text)
	}
	glyph, tip := "copy", "复制代码"
	if copied {
		glyph, tip = "check", "已复制"
		btn.TextColor(c.Theme().Accent)
	}
	btn.Label(tip).Children(func() { icon(c, glyph) }).Tooltip(tip)
	if btn.Clicked() {
		c.WriteClipboard(code)
		*copiedAt = c.Now()
		c.After(2 * time.Second)
	}
	return btn
}

func safeMarkdownLink(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || strings.ContainsAny(raw, "\r\n") {
		return false
	}
	return ((u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "") || (u.Scheme == "mailto" && u.Opaque != "")
}

func largeRichText(text string) bool { return len(text) > 64<<10 || strings.Count(text, "\n") > 400 }

type sourceViewState struct {
	text   string
	editor *editor.State
}

func nativeSource(c *ui.Context, text string, height float32, label string) *editor.State {
	box := ui.Column(c).MinWidth(0)
	state := ui.Local(box, "source", func() sourceViewState {
		return sourceViewState{editor: &editor.State{Buffer: editor.NewBuffer(""), ReadOnly: true, NoScrollPastEnd: true}}
	})
	if state.text != text {
		state.text = text
		state.editor.Buffer.Reset(text)
	}
	box.Children(func() { editor.View(c, state.editor).Height(height).FillWidth().Label(label) })
	return state.editor
}
