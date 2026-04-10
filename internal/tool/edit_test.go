package tool_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/tool"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0644))
	return p
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestEdit_SingleReplacement(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package main\n\nfunc main() { println(\"hi\") }\n")

	e := tool.NewEditTool(dir)
	res, err := e.Edit(context.Background(), tool.EditRequest{
		Path:  "a.go",
		Edits: []tool.EditOp{{OldText: `println("hi")`, NewText: `println("bye")`}},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, res.EditsApplied)
	assert.Contains(t, readFile(t, filepath.Join(dir, "a.go")), `println("bye")`)
	assert.NotEmpty(t, res.Diff)
	assert.Greater(t, res.FirstChangedLine, 0)
}

func TestEdit_MultiEditHappyPath(t *testing.T) {
	dir := t.TempDir()
	content := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	writeFile(t, dir, "f.txt", content)

	e := tool.NewEditTool(dir)
	_, err := e.Edit(context.Background(), tool.EditRequest{
		Path: "f.txt",
		Edits: []tool.EditOp{
			{OldText: "alpha", NewText: "ALPHA"},
			{OldText: "gamma", NewText: "GAMMA"},
			{OldText: "epsilon", NewText: "EPSILON"},
		},
	})
	require.NoError(t, err)
	got := readFile(t, filepath.Join(dir, "f.txt"))
	assert.Equal(t, "ALPHA\nbeta\nGAMMA\ndelta\nEPSILON\n", got)
}

func TestEdit_OverlapRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "the quick brown fox")

	e := tool.NewEditTool(dir)
	_, err := e.Edit(context.Background(), tool.EditRequest{
		Path: "f.txt",
		Edits: []tool.EditOp{
			{OldText: "quick brown", NewText: "slow"},
			{OldText: "brown fox", NewText: "red cat"},
		},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "overlap")
}

func TestEdit_NotFound(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "hello")

	e := tool.NewEditTool(dir)
	_, err := e.Edit(context.Background(), tool.EditRequest{
		Path:  "f.txt",
		Edits: []tool.EditOp{{OldText: "missing", NewText: "x"}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "could not find")
}

func TestEdit_DuplicateMatchRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "foo foo foo")

	e := tool.NewEditTool(dir)
	_, err := e.Edit(context.Background(), tool.EditRequest{
		Path:  "f.txt",
		Edits: []tool.EditOp{{OldText: "foo", NewText: "bar"}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "occurrences")
}

func TestEdit_FuzzyFallbackSmartQuotes(t *testing.T) {
	dir := t.TempDir()
	// File has smart quotes; model supplies ASCII quotes.
	writeFile(t, dir, "f.txt", "title = \u201cFoo\u201d;\nother\n")

	e := tool.NewEditTool(dir)
	_, err := e.Edit(context.Background(), tool.EditRequest{
		Path:  "f.txt",
		Edits: []tool.EditOp{{OldText: `title = "Foo";`, NewText: `title = "Bar";`}},
	})
	require.NoError(t, err)
	got := readFile(t, filepath.Join(dir, "f.txt"))
	// After fuzzy fallback, the file is rewritten from the normalized
	// base, so quotes become ASCII.
	assert.Contains(t, got, `title = "Bar";`)
	assert.NotContains(t, got, "\u201c")
}

func TestEdit_CRLFPreservation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "line1\r\nline2\r\nline3\r\n")

	e := tool.NewEditTool(dir)
	_, err := e.Edit(context.Background(), tool.EditRequest{
		Path:  "f.txt",
		Edits: []tool.EditOp{{OldText: "line2", NewText: "LINE2"}},
	})
	require.NoError(t, err)
	got := readFile(t, filepath.Join(dir, "f.txt"))
	assert.Equal(t, "line1\r\nLINE2\r\nline3\r\n", got)
}

func TestEdit_BOMPreservation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "\uFEFFhello\nworld\n")

	e := tool.NewEditTool(dir)
	_, err := e.Edit(context.Background(), tool.EditRequest{
		Path:  "f.txt",
		Edits: []tool.EditOp{{OldText: "hello", NewText: "HELLO"}},
	})
	require.NoError(t, err)
	got := readFile(t, filepath.Join(dir, "f.txt"))
	assert.True(t, strings.HasPrefix(got, "\uFEFF"))
	assert.Contains(t, got, "HELLO")
}

func TestEdit_EmptyOldText(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "hello")

	e := tool.NewEditTool(dir)
	_, err := e.Edit(context.Background(), tool.EditRequest{
		Path:  "f.txt",
		Edits: []tool.EditOp{{OldText: "", NewText: "x"}},
	})
	assert.Error(t, err)
}

func TestEdit_UnchangedContentRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "hello world")

	e := tool.NewEditTool(dir)
	_, err := e.Edit(context.Background(), tool.EditRequest{
		Path:  "f.txt",
		Edits: []tool.EditOp{{OldText: "hello", NewText: "hello"}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no changes")
}

func TestEdit_JSONArgs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "foo\nbar\n")

	e := tool.NewEditTool(dir)
	res, err := e.Execute(context.Background(), map[string]interface{}{
		"path": "f.txt",
		"edits": []interface{}{
			map[string]interface{}{"old_text": "foo", "new_text": "FOO"},
		},
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, res, "Successfully applied 1 edit")
	assert.Contains(t, readFile(t, filepath.Join(dir, "f.txt")), "FOO")
}
