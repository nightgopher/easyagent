package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readFileResult(t *testing.T, dir, name string) (string, []byte, bool, string) {
	t.Helper()
	tool := NewReadTool(WithReadWorkspace(dir))
	raw, err := tool.Validate(json.RawMessage(`{"path":"` + name + `"}`))
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), raw, nil)
	require.NoError(t, err)
	if len(result.Images) == 0 {
		return result.Content, nil, result.IsError, ""
	}
	return result.Content, result.Images[0].Data, result.IsError, result.Images[0].MediaType
}

func TestReadReturnsPNGAsImage(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shot.png"), buf.Bytes(), 0o600))

	content, data, isErr, mediaType := readFileResult(t, dir, "shot.png")
	assert.False(t, isErr)
	assert.Equal(t, "image/png", mediaType)
	assert.Equal(t, buf.Bytes(), data)
	assert.Contains(t, content, "Image file shot.png")
}

func TestReadReturnsJPEGAsImage(t *testing.T) {
	dir := t.TempDir()
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "photo.jpg"), jpeg, 0o600))

	_, data, isErr, mediaType := readFileResult(t, dir, "photo.jpg")
	assert.False(t, isErr)
	assert.Equal(t, "image/jpeg", mediaType)
	assert.Equal(t, jpeg, data)
}

func TestReadRejectsOversizedImage(t *testing.T) {
	dir := t.TempDir()
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxReadImageBytes)...)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big.png"), big, 0o600))

	content, data, isErr, _ := readFileResult(t, dir, "big.png")
	assert.True(t, isErr)
	assert.Nil(t, data)
	assert.Contains(t, content, "byte limit")
}

func TestReadDescribesBinaryInsteadOfDumpingBytes(t *testing.T) {
	dir := t.TempDir()
	bin := []byte{0x7F, 'E', 'L', 'F', 0x02, 0x01, 0x00, 0x00, 0xDE, 0xAD}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.out"), bin, 0o600))

	content, data, isErr, _ := readFileResult(t, dir, "a.out")
	assert.False(t, isErr)
	assert.Nil(t, data)
	assert.Contains(t, content, "Binary file a.out")
	assert.Contains(t, content, "10 bytes")
	assert.NotContains(t, content, "ELF")
}

func TestReadDescribesPDF(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "doc.pdf"), []byte("%PDF-1.7\n1 0 obj\n"), 0o600))

	content, _, _, _ := readFileResult(t, dir, "doc.pdf")
	assert.Contains(t, content, "application/pdf")
	assert.Contains(t, content, "pdftotext")
}

func TestReadKeepsNonUTF8TextAsText(t *testing.T) {
	dir := t.TempDir()
	gbk := []byte{0xC4, 0xE3, 0xBA, 0xC3, '\n'} // "你好" in GBK
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gbk.txt"), gbk, 0o600))

	content, _, _, _ := readFileResult(t, dir, "gbk.txt")
	assert.Contains(t, content, "     1\t")
	assert.NotContains(t, content, "Binary file")
}
