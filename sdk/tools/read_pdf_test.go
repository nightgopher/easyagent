package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildPDF 生成每页一行文本的最小合法 PDF；texts 中的空串生成无文本层（扫描页）的页。
func buildPDF(texts []string) []byte {
	var buf bytes.Buffer
	offsets := map[int]int{}
	writeObj := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}

	buf.WriteString("%PDF-1.4\n")
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, len(texts))
	for i := range texts {
		kids[i] = fmt.Sprintf("%d 0 R", 3+2*i)
	}
	writeObj(2, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>",
		strings.Join(kids, " "), len(texts)))
	fontObj := 3 + 2*len(texts)
	for i, text := range texts {
		pageObj, contentObj := 3+2*i, 4+2*i
		writeObj(pageObj, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			fontObj, contentObj))
		stream := "q Q" // 无文本操作：模拟扫描页
		if text != "" {
			stream = fmt.Sprintf("BT /F1 12 Tf 20 100 Td (%s) Tj ET", text)
		}
		offsets[contentObj] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
			contentObj, len(stream), stream)
	}
	writeObj(fontObj, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", fontObj+1)
	for i := 1; i <= fontObj; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		fontObj+1, xrefStart)
	return buf.Bytes()
}

func writePDF(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.pdf")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func executeReadResult(t *testing.T, tool *ReadTool, args string) (string, bool) {
	t.Helper()
	raw, err := tool.Validate(json.RawMessage(args))
	if err != nil {
		return err.Error(), true
	}
	result, _ := tool.Execute(context.Background(), raw, nil)
	return result.Content, result.IsError
}

func TestReadPDFExtractsRequestedPages(t *testing.T) {
	pdf := writePDF(t, buildPDF([]string{"alpha page one", "beta page two", "gamma page three"}))
	tool := NewReadTool()

	content, isErr := executeReadResult(t, tool, fmt.Sprintf(`{"path": %q, "pages": "1-2"}`, pdf))
	require.False(t, isErr, content)
	assert.Contains(t, content, "3 pages, showing pages 1-2")
	assert.Contains(t, content, "alpha page one")
	assert.Contains(t, content, "beta page two")
	assert.NotContains(t, content, "gamma page three")

	content, isErr = executeReadResult(t, tool, fmt.Sprintf(`{"path": %q, "pages": "2"}`, pdf))
	require.False(t, isErr, content)
	assert.Contains(t, content, "beta page two")
	assert.NotContains(t, content, "alpha page one")
}

func TestReadPDFDefaultsToFirstPagesWithContinuation(t *testing.T) {
	texts := make([]string, 25)
	for i := range texts {
		texts[i] = fmt.Sprintf("page marker %02d", i+1)
	}
	pdf := writePDF(t, buildPDF(texts))
	tool := NewReadTool()

	content, isErr := executeReadResult(t, tool, fmt.Sprintf(`{"path": %q}`, pdf))
	require.False(t, isErr, content)
	assert.Contains(t, content, "25 pages, showing pages 1-10")
	assert.Contains(t, content, "page marker 10")
	assert.NotContains(t, content, "page marker 11")
	assert.Contains(t, content, "15 more pages (use pages=11-20")

	content, isErr = executeReadResult(t, tool, fmt.Sprintf(`{"path": %q, "pages": "21-25"}`, pdf))
	require.False(t, isErr, content)
	assert.Contains(t, content, "page marker 25")
}

func TestReadPDFRejectsInvalidPageSpecs(t *testing.T) {
	pdf := writePDF(t, buildPDF([]string{"only page"}))
	tool := NewReadTool()

	for _, spec := range []string{`"pages": "0"`, `"pages": "5-1"`, `"pages": "abc"`, `"pages": "1-51"`, `"pages": "3,1"`} {
		content, isErr := executeReadResult(t, tool, fmt.Sprintf(`{"path": %q, %s}`, pdf, spec))
		require.True(t, isErr, spec)
		assert.Contains(t, content, "pages", spec)
	}

	content, isErr := executeReadResult(t, tool, fmt.Sprintf(`{"path": %q, "pages": "9"}`, pdf))
	require.True(t, isErr)
	assert.Contains(t, content, "has 1 pages; page 9 does not exist")
}

func TestReadPDFNotesScannedPages(t *testing.T) {
	pdf := writePDF(t, buildPDF([]string{"", "has text layer"}))
	tool := NewReadTool()

	content, isErr := executeReadResult(t, tool, fmt.Sprintf(`{"path": %q}`, pdf))
	require.False(t, isErr, content)
	assert.Contains(t, content, "--- Page 1 ---")
	assert.Contains(t, content, "no extractable text")
	assert.Contains(t, content, "has text layer")

	scannedOnly := writePDF(t, buildPDF([]string{"", ""}))
	content, isErr = executeReadResult(t, tool, fmt.Sprintf(`{"path": %q}`, scannedOnly))
	require.False(t, isErr, content)
	assert.Contains(t, content, "No text layer found")
	assert.Contains(t, content, "scanned or image-only")
}

func TestReadPDFReportsMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.pdf")
	require.NoError(t, os.WriteFile(path, []byte("%PDF-1.4 this is not a real pdf"), 0o600))
	tool := NewReadTool()

	content, isErr := executeReadResult(t, tool, fmt.Sprintf(`{"path": %q}`, path))
	require.True(t, isErr, content)
	assert.Contains(t, content, "cannot read PDF")
}

func TestReadPDFCapsOutputOnPageBoundary(t *testing.T) {
	texts := make([]string, 6)
	for i := range texts {
		texts[i] = fmt.Sprintf("marker %d %s", i+1, strings.Repeat("x", 60))
	}
	pdf := writePDF(t, buildPDF(texts))
	tool := NewReadTool(WithReadMaxOutputLen(200))

	content, isErr := executeReadResult(t, tool, fmt.Sprintf(`{"path": %q, "pages": "1-6"}`, pdf))
	require.False(t, isErr, content)
	assert.Contains(t, content, "marker 1")
	assert.NotContains(t, content, "marker 6")
	assert.Contains(t, content, "output capped at 200 characters after page")
	assert.Contains(t, content, "continue with pages=")
}
