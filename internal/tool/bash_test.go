package tool_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/tool"
)

func TestBash_Basic(t *testing.T) {
	b := &tool.Bash{Cwd: t.TempDir()}
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "echo hello",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "hello")
}

func TestBash_NoCommand(t *testing.T) {
	b := &tool.Bash{Cwd: t.TempDir()}
	_, err := b.Execute(context.Background(), map[string]interface{}{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "command is required")
}

func TestBash_NonZeroExit(t *testing.T) {
	b := &tool.Bash{Cwd: t.TempDir()}
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "exit 42",
	})
	// Non-zero exit is not an error — it's returned in the output.
	require.NoError(t, err)
	assert.Contains(t, result, "exited with code 42")
}

func TestBash_Stderr(t *testing.T) {
	b := &tool.Bash{Cwd: t.TempDir()}
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "echo error >&2",
	})
	require.NoError(t, err)
	assert.Contains(t, result, "error")
}

func TestBash_Timeout(t *testing.T) {
	b := &tool.Bash{Cwd: t.TempDir()}
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "sleep 60",
		"timeout": float64(1),
	})
	require.NoError(t, err)
	assert.Contains(t, result, "timed out")
}

func TestBash_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := &tool.Bash{Cwd: t.TempDir()}

	// Cancel immediately.
	cancel()

	result, err := b.Execute(ctx, map[string]interface{}{
		"command": "sleep 60",
	})
	// Could be error or result depending on timing.
	if err != nil {
		assert.Contains(t, err.Error(), "context canceled")
	} else {
		assert.Contains(t, result, "aborted")
	}
}

func TestBash_Cwd(t *testing.T) {
	dir := t.TempDir()
	b := &tool.Bash{Cwd: dir}
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "pwd",
	})
	require.NoError(t, err)
	assert.Contains(t, result, dir)
}

func TestBash_NoOutput(t *testing.T) {
	b := &tool.Bash{Cwd: t.TempDir()}
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "true",
	})
	require.NoError(t, err)
	assert.Equal(t, "(no output)", result)
}

func TestBash_OutputTruncation(t *testing.T) {
	b := &tool.Bash{Cwd: t.TempDir()}
	// Generate more than MaxOutputLines.
	cmd := "for i in $(seq 1 3000); do echo line$i; done"
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": cmd,
	})
	require.NoError(t, err)
	assert.Contains(t, result, "[Output truncated")
	// Should contain the last lines, not the first.
	assert.Contains(t, result, "line3000")
	assert.NotContains(t, result, "line1\n")
}

func TestBash_LargeOutputByteTruncation(t *testing.T) {
	b := &tool.Bash{Cwd: t.TempDir()}
	// Generate more than MaxOutputBytes.
	// Each line is ~80 chars, need 50KB / 80 ≈ 640 lines, but use more to be safe.
	cmd := "for i in $(seq 1 2000); do echo $(head -c 80 /dev/urandom | base64 | head -c 80); done"
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": cmd,
	})
	require.NoError(t, err)
	// Should be truncated.
	lines := strings.Split(result, "\n")
	totalBytes := len(result)
	// Allow some slack for the truncation message.
	assert.LessOrEqual(t, totalBytes, tool.MaxOutputBytes+200)
	_ = lines
}

func TestBash_ProcessTreeKill(t *testing.T) {
	b := &tool.Bash{Cwd: t.TempDir()}
	// Spawn a child that spawns a grandchild. Timeout should kill both.
	result, err := b.Execute(context.Background(), map[string]interface{}{
		"command": "bash -c 'sleep 60' & sleep 60",
		"timeout": float64(1),
	})
	require.NoError(t, err)
	assert.Contains(t, result, "timed out")
}

func TestBash_Name(t *testing.T) {
	b := &tool.Bash{}
	assert.Equal(t, "bash", b.Name())
}

func TestBash_Description(t *testing.T) {
	b := &tool.Bash{}
	assert.Contains(t, b.Description(), "2000 lines")
	assert.Contains(t, b.Description(), "50KB")
}
