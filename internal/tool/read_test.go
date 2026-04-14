package tool_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/tool"
)

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

// readResultText extracts concatenated text from a read tool result.
func readResultText(content []message.Content) string {
	var sb strings.Builder
	for _, c := range content {
		if c.Type == message.ContentText {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

func TestRead_Basic(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "hello.txt", "hello world")

	r := tool.NewReadTool(dir)
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path": "hello.txt",
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, readResultText(result), "hello world")
}

func TestRead_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "abs.txt", "absolute")

	r := tool.NewReadTool("/tmp")
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path": path,
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, readResultText(result), "absolute")
}

func TestRead_NoPath(t *testing.T) {
	r := tool.NewReadTool(t.TempDir())
	_, err := r.Execute(context.Background(), map[string]interface{}{}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path is required")
}

func TestRead_FileNotFound(t *testing.T) {
	r := tool.NewReadTool(t.TempDir())
	_, err := r.Execute(context.Background(), map[string]interface{}{
		"path": "nonexistent.txt",
	}, nil)
	assert.Error(t, err)
}

func TestRead_Offset(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "lines.txt", "line1\nline2\nline3\nline4\nline5")

	r := tool.NewReadTool(dir)
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path":   "lines.txt",
		"offset": float64(3),
	}, nil)
	require.NoError(t, err)
	text := readResultText(result)
	assert.Contains(t, text, "line3")
	assert.Contains(t, text, "line4")
	assert.Contains(t, text, "line5")
	assert.NotContains(t, text, "line1\n")
	assert.NotContains(t, text, "line2\n")
}

func TestRead_Limit(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "lines.txt", "line1\nline2\nline3\nline4\nline5")

	r := tool.NewReadTool(dir)
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path":  "lines.txt",
		"limit": float64(2),
	}, nil)
	require.NoError(t, err)
	text := readResultText(result)
	assert.Contains(t, text, "line1")
	assert.Contains(t, text, "line2")
	assert.Contains(t, text, "more lines in file")
	assert.Contains(t, text, "offset=3")
}

func TestRead_OffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "lines.txt", "line1\nline2\nline3\nline4\nline5")

	r := tool.NewReadTool(dir)
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path":   "lines.txt",
		"offset": float64(2),
		"limit":  float64(2),
	}, nil)
	require.NoError(t, err)
	text := readResultText(result)
	assert.Contains(t, text, "line2")
	assert.Contains(t, text, "line3")
	assert.Contains(t, text, "more lines in file")
	assert.Contains(t, text, "offset=4")
}

func TestRead_OffsetBeyondEnd(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "short.txt", "one\ntwo")

	r := tool.NewReadTool(dir)
	_, err := r.Execute(context.Background(), map[string]interface{}{
		"path":   "short.txt",
		"offset": float64(100),
	}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "beyond end of file")
}

func TestRead_LineTruncation(t *testing.T) {
	dir := t.TempDir()
	// Generate more than MaxOutputLines.
	var lines []string
	for i := 1; i <= 3000; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	writeTestFile(t, dir, "big.txt", strings.Join(lines, "\n"))

	r := tool.NewReadTool(dir)
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path": "big.txt",
	}, nil)
	require.NoError(t, err)
	text := readResultText(result)
	assert.Contains(t, text, "line1\n")       // head preserved
	assert.NotContains(t, text, "line3000\n") // tail truncated
	assert.Contains(t, text, "Use offset=")
}

func TestRead_ByteTruncation(t *testing.T) {
	dir := t.TempDir()
	// Many lines, each small, total > MaxOutputBytes. Byte limit hits
	// before line limit.
	var lines []string
	for i := 0; i < 600; i++ {
		lines = append(lines, strings.Repeat("x", 100))
	}
	writeTestFile(t, dir, "wide.txt", strings.Join(lines, "\n"))

	r := tool.NewReadTool(dir)
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path": "wide.txt",
	}, nil)
	require.NoError(t, err)
	text := readResultText(result)
	assert.LessOrEqual(t, len(text), tool.MaxOutputBytes+200) // allow slack for continuation message
	assert.Contains(t, text, "Use offset=")
}

func TestRead_FirstLineExceedsLimit(t *testing.T) {
	dir := t.TempDir()
	// Single line larger than MaxOutputBytes — TruncateHead returns
	// FirstLineExceedsLimit, and the read tool surfaces a sed fallback
	// instead of a mid-line slice.
	bigLine := strings.Repeat("x", 60*1024)
	writeTestFile(t, dir, "huge.txt", bigLine)

	r := tool.NewReadTool(dir)
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path": "huge.txt",
	}, nil)
	require.NoError(t, err)
	text := readResultText(result)
	assert.Contains(t, text, "exceeds")
	assert.Contains(t, text, "sed -n '1p'")
	assert.Contains(t, text, "huge.txt")
	// No output lines emitted — only the hint.
	assert.Less(t, len(text), 300)
}

func TestRead_Name(t *testing.T) {
	r := tool.NewReadTool("")
	assert.Equal(t, "read", r.Name())
}

func TestRead_Description(t *testing.T) {
	r := tool.NewReadTool("")
	assert.Contains(t, r.Description(), "2000 lines")
	assert.Contains(t, r.Description(), "offset")
}

func TestRead_Image(t *testing.T) {
	dir := t.TempDir()
	// Minimal valid PNG: 1x1 pixel, red.
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, // 8-bit RGB
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, // IDAT chunk
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x00, 0x02, 0x00, 0x01, 0xe2, 0x21, 0xbc,
		0x33, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, // IEND chunk
		0x44, 0xae, 0x42, 0x60, 0x82,
	}
	path := filepath.Join(dir, "test.png")
	require.NoError(t, os.WriteFile(path, png, 0644))

	r := tool.NewReadTool(dir)
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path": "test.png",
	}, nil)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, message.ContentText, result[0].Type)
	assert.Contains(t, result[0].Text, "image/png")
	assert.Equal(t, message.ContentImage, result[1].Type)
	assert.Equal(t, "image/png", result[1].MimeType)
	assert.NotEmpty(t, result[1].Data)
}
