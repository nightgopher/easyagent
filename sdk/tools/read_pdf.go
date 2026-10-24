package tools

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/hwj123hwj/easyagent/sdk/agent"
	"github.com/ledongthuc/pdf"
)

const (
	// defaultReadPDFPages 是未指定 pages 时读取的页数；maxReadPDFPages 是单次 read 的页数上限，
	// 避免整本书一次塞进上下文。
	defaultReadPDFPages = 10
	maxReadPDFPages     = 50
)

// parsePDFPageRange 解析 "3" 或 "1-5"（页码从 1 起）。
func parsePDFPageRange(spec string) (start, end int, err error) {
	invalid := fmt.Errorf("invalid pages %q: use a page number or a range such as \"3\" or \"1-5\" (at most %d pages per call)", spec, maxReadPDFPages)
	lo, hi, isRange := strings.Cut(strings.TrimSpace(spec), "-")
	if start, err = strconv.Atoi(strings.TrimSpace(lo)); err != nil || start < 1 {
		return 0, 0, invalid
	}
	end = start
	if isRange {
		if end, err = strconv.Atoi(strings.TrimSpace(hi)); err != nil || end < start || end-start+1 > maxReadPDFPages {
			return 0, 0, invalid
		}
	}
	return start, end, nil
}

// validatePDFPagesSpec 在 Validate 阶段检查 pages 的格式；空串表示使用默认范围。
func validatePDFPagesSpec(spec string) error {
	if strings.TrimSpace(spec) == "" {
		return nil
	}
	_, _, err := parsePDFPageRange(spec)
	return err
}

// readPDF 按页抽取 PDF 文字。无法解析（加密、损坏）时返回错误结果，提示改用 bash 转换。
func (t *ReadTool) readPDF(path string, data []byte, pagesSpec string) (result agent.ToolResult) {
	cannotRead := func(reason string) agent.ToolResult {
		return agent.ToolResult{IsError: true, Content: fmt.Sprintf(
			"cannot read PDF %s (application/pdf, %d bytes): %s; try a converter such as pdftotext through bash if available",
			path, len(data), reason)}
	}
	// 解析库遇到畸形文件可能 panic，不能让它带崩会话。
	defer func() {
		if r := recover(); r != nil {
			result = cannotRead(fmt.Sprint("parser panic: ", r))
		}
	}()

	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return cannotRead(err.Error())
	}
	total := reader.NumPage()
	if total <= 0 {
		return cannotRead("no pages found")
	}

	explicit := strings.TrimSpace(pagesSpec) != ""
	start, end := 1, min(total, defaultReadPDFPages)
	if explicit {
		var err error
		if start, end, err = parsePDFPageRange(pagesSpec); err != nil {
			return agent.ToolResult{IsError: true, Content: err.Error()}
		}
		if end > total {
			missing := max(start, total+1)
			return agent.ToolResult{IsError: true, Content: fmt.Sprintf("PDF %s has %d pages; page %d does not exist", path, total, missing)}
		}
	}

	maxLen := t.maxOutputLen
	if maxLen <= 0 {
		maxLen = DefaultMaxOutputLen
	}

	var b strings.Builder
	shown, hasText := 0, false
	for num := start; num <= end; num++ {
		text, err := reader.Page(num).GetPlainText(nil) // 字体资源名按页独立，不跨页共享缓存
		switch text = strings.TrimSpace(text); {
		case err != nil:
			text = fmt.Sprintf("(page %d: text could not be extracted: %v)", num, err)
		case text == "":
			text = fmt.Sprintf("(page %d: no extractable text)", num)
		default:
			hasText = true
		}
		section := fmt.Sprintf("--- Page %d ---\n%s\n\n", num, text)
		if len(section) > maxLen {
			section = truncateByteCap(section, maxLen) // 单页超限：硬切该页，停在页尾
		} else if shown > 0 && b.Len()+len(section) > maxLen {
			break // 剩余页放不下，在页边界收尾，续读提示在循环外补
		}
		b.WriteString(section)
		shown++
	}

	last := start + shown - 1
	var out strings.Builder
	fmt.Fprintf(&out, "PDF file %s: %d pages, showing pages %s.\n\n%s", path, total, formatPDFPageRange(start, last), b.String())
	switch {
	case last < end:
		fmt.Fprintf(&out, "... output capped at %d characters after page %d; continue with pages=%d-%d (fewer pages per call when pages are dense)\n",
			maxLen, last, last+1, end)
	case !explicit && total > end:
		fmt.Fprintf(&out, "... %d more pages (use pages=%d-%d to read further)\n", total-end, end+1, min(total, end+defaultReadPDFPages))
	}
	if !hasText {
		out.WriteString("\nNo text layer found: this PDF looks scanned or image-only and needs OCR (for example ocrmypdf or tesseract through bash if available).\n")
	}
	return agent.ToolResult{Content: out.String()}
}

func formatPDFPageRange(start, end int) string {
	if start == end {
		return strconv.Itoa(start)
	}
	return fmt.Sprintf("%d-%d", start, end)
}
