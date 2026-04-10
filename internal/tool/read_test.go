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

	"github.com/jack-work/figaro/internal/tool"
)

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestRead_Basic(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "hello.txt", "hello world")

	r := tool.NewReadTool(dir)
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path": "hello.txt",
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, result, "hello world")
}

func TestRead_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "abs.txt", "absolute")

	r := tool.NewReadTool("/tmp")
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path": path,
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, result, "absolute")
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
	assert.Contains(t, result, "line3")
	assert.Contains(t, result, "line4")
	assert.Contains(t, result, "line5")
	assert.NotContains(t, result, "line1\n")
	assert.NotContains(t, result, "line2\n")
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
	assert.Contains(t, result, "line1")
	assert.Contains(t, result, "line2")
	assert.Contains(t, result, "more lines in file")
	assert.Contains(t, result, "offset=3")
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
	assert.Contains(t, result, "line2")
	assert.Contains(t, result, "line3")
	assert.Contains(t, result, "more lines in file")
	assert.Contains(t, result, "offset=4")
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
	assert.Contains(t, result, "line1\n")       // head preserved
	assert.NotContains(t, result, "line3000\n") // tail truncated
	assert.Contains(t, result, "Use offset=")
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
	assert.LessOrEqual(t, len(result), tool.MaxOutputBytes+200) // allow slack for continuation message
	assert.Contains(t, result, "Use offset=")
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
	assert.Contains(t, result, "exceeds")
	assert.Contains(t, result, "sed -n '1p'")
	assert.Contains(t, result, "huge.txt")
	// No output lines emitted — only the hint.
	assert.Less(t, len(result), 300)
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
