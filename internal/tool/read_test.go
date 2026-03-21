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

	r := &tool.Read{Cwd: dir}
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path": "hello.txt",
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, result, "hello world")
}

func TestRead_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "abs.txt", "absolute")

	r := &tool.Read{Cwd: "/tmp"}
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path": path,
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, result, "absolute")
}

func TestRead_NoPath(t *testing.T) {
	r := &tool.Read{Cwd: t.TempDir()}
	_, err := r.Execute(context.Background(), map[string]interface{}{}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "path is required")
}

func TestRead_FileNotFound(t *testing.T) {
	r := &tool.Read{Cwd: t.TempDir()}
	_, err := r.Execute(context.Background(), map[string]interface{}{
		"path": "nonexistent.txt",
	}, nil)
	assert.Error(t, err)
}

func TestRead_Offset(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "lines.txt", "line1\nline2\nline3\nline4\nline5")

	r := &tool.Read{Cwd: dir}
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

	r := &tool.Read{Cwd: dir}
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

	r := &tool.Read{Cwd: dir}
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

	r := &tool.Read{Cwd: dir}
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

	r := &tool.Read{Cwd: dir}
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
	// Generate more than MaxOutputBytes in few lines.
	bigLine := strings.Repeat("x", 60*1024) // 60KB in one line
	writeTestFile(t, dir, "huge.txt", bigLine)

	r := &tool.Read{Cwd: dir}
	result, err := r.Execute(context.Background(), map[string]interface{}{
		"path": "huge.txt",
	}, nil)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(result), tool.MaxOutputBytes+200) // allow slack for message
	assert.Contains(t, result, "Use offset=")
}

func TestRead_Name(t *testing.T) {
	r := &tool.Read{}
	assert.Equal(t, "read", r.Name())
}

func TestRead_Description(t *testing.T) {
	r := &tool.Read{}
	assert.Contains(t, r.Description(), "2000 lines")
	assert.Contains(t, r.Description(), "offset")
}
