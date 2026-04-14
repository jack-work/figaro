package tool_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/tool"
)

// resultText extracts the concatenated text from a tool result.
func resultText(content []message.Content) string {
	var sb strings.Builder
	for _, c := range content {
		if c.Type == message.ContentText {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

func TestBash_Basic(t *testing.T) {
	b := tool.NewBashTool(t.TempDir())
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "echo hello",
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, resultText(result), "hello")
}

func TestBash_NoCommand(t *testing.T) {
	b := tool.NewBashTool(t.TempDir())
	_, err := b.Execute(context.Background(), map[string]interface{}{}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "command is required")
}

func TestBash_NonZeroExit(t *testing.T) {
	b := tool.NewBashTool(t.TempDir())
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "exit 42",
	}, nil)
	// Non-zero exit is not an error — it's returned in the output.
	require.NoError(t, err)
	assert.Contains(t, resultText(result), "exited with code 42")
}

func TestBash_Stderr(t *testing.T) {
	b := tool.NewBashTool(t.TempDir())
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "echo error >&2",
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, resultText(result), "error")
}

func TestBash_Timeout(t *testing.T) {
	b := tool.NewBashTool(t.TempDir())
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "sleep 60",
		"timeout": float64(1),
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, resultText(result), "timed out")
}

func TestBash_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := tool.NewBashTool(t.TempDir())

	// Cancel immediately.
	cancel()

	result, err := b.Execute(ctx, map[string]interface{}{
		"command": "sleep 60",
	}, nil)
	// Could be error or result depending on timing.
	if err != nil {
		assert.Contains(t, err.Error(), "context canceled")
	} else {
		assert.Contains(t, resultText(result), "aborted")
	}
}

func TestBash_Cwd(t *testing.T) {
	dir := t.TempDir()
	b := tool.NewBashTool(dir)
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "pwd",
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, resultText(result), dir)
}

func TestBash_NoOutput(t *testing.T) {
	b := tool.NewBashTool(t.TempDir())
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "true",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "(no output)", resultText(result))
}

func TestBash_OutputTruncation(t *testing.T) {
	b := tool.NewBashTool(t.TempDir())
	// Generate more than MaxOutputLines.
	cmd := "for i in $(seq 1 3000); do echo line$i; done"
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": cmd,
	}, nil)
	require.NoError(t, err)
	text := resultText(result)
	assert.Contains(t, text, "[Output truncated")
	// Should contain the last lines, not the first.
	assert.Contains(t, text, "line3000")
	assert.NotContains(t, text, "line1\n")
}

func TestBash_LargeOutputByteTruncation(t *testing.T) {
	b := tool.NewBashTool(t.TempDir())
	// Generate more than MaxOutputBytes.
	// Each line is ~80 chars, need 50KB / 80 ≈ 640 lines, but use more to be safe.
	cmd := "for i in $(seq 1 2000); do echo $(head -c 80 /dev/urandom | base64 | head -c 80); done"
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": cmd,
	}, nil)
	require.NoError(t, err)
	text := resultText(result)
	// Should be truncated.
	lines := strings.Split(text, "\n")
	totalBytes := len(text)
	// Allow some slack for the truncation message.
	assert.LessOrEqual(t, totalBytes, tool.MaxOutputBytes+200)
	_ = lines
}

func TestBash_ProcessTreeKill(t *testing.T) {
	b := tool.NewBashTool(t.TempDir())
	// Spawn a child that spawns a grandchild. Timeout should kill both.
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "bash -c 'sleep 60' & sleep 60",
		"timeout": float64(1),
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, resultText(result), "timed out")
}

func TestBash_StreamingOutput(t *testing.T) {
	b := tool.NewBashTool(t.TempDir())

	var chunks []string
	onOutput := func(chunk []byte) {
		chunks = append(chunks, string(chunk))
	}

	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "echo hello; echo world",
	}, onOutput)
	require.NoError(t, err)

	text := resultText(result)
	// Final result should contain both lines.
	assert.Contains(t, text, "hello")
	assert.Contains(t, text, "world")

	// Streaming callback should have been called with chunks.
	assert.NotEmpty(t, chunks, "onOutput should have been called")

	// Reassembled chunks should equal the final result.
	reassembled := strings.Join(chunks, "")
	assert.Equal(t, text, reassembled)
}

func TestBash_StreamingNil(t *testing.T) {
	b := tool.NewBashTool(t.TempDir())
	// nil onOutput should not panic.
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "echo ok",
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, resultText(result), "ok")
}

func TestBash_Name(t *testing.T) {
	b := tool.NewBashTool("")
	assert.Equal(t, "bash", b.Name())
}

func TestBash_Description(t *testing.T) {
	b := tool.NewBashTool("")
	assert.Contains(t, b.Description(), "2000 lines")
	assert.Contains(t, b.Description(), "50KB")
}
