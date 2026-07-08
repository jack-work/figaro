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

// write streams the content preview line-by-line (like bash stdout) while the
// file itself is written atomically; the returned Content is the summary, not
// the streamed content.
func TestWrite_StreamsContentLineByLine(t *testing.T) {
	dir := t.TempDir()
	w := tool.NewWriteTool(dir)
	path := filepath.Join(dir, "out.txt")
	content := "alpha\nbeta\ngamma\n"

	var chunks []string
	onOutput := func(chunk []byte) { chunks = append(chunks, string(chunk)) }

	result, err := w.Execute(context.Background(), map[string]interface{}{
		"path":    path,
		"content": content,
	}, onOutput)
	require.NoError(t, err)

	// File written atomically with the exact content.
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, string(got))

	// Streamed as separate line chunks, reassembling to the content.
	assert.GreaterOrEqual(t, len(chunks), 3, "content should stream line-by-line")
	assert.Equal(t, content, strings.Join(chunks, ""))

	// The returned Content is the summary, not the content preview.
	summary := resultText(result)
	assert.Contains(t, summary, "Wrote")
	assert.NotContains(t, summary, "alpha")
}

func TestWrite_StreamingNil(t *testing.T) {
	dir := t.TempDir()
	w := tool.NewWriteTool(dir)
	// nil onOutput must not panic.
	_, err := w.Execute(context.Background(), map[string]interface{}{
		"path":    filepath.Join(dir, "x.txt"),
		"content": "one\ntwo\n",
	}, nil)
	require.NoError(t, err)
}
