package tool_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jack-work/figaro/internal/tool"
)

func TestTruncateHead_UnderBothLimits(t *testing.T) {
	in := "line1\nline2\nline3"
	r := tool.TruncateHead(in, tool.TruncationOptions{})
	assert.False(t, r.Truncated)
	assert.Equal(t, tool.TruncatedByNone, r.TruncatedBy)
	assert.Equal(t, 3, r.TotalLines)
	assert.Equal(t, 3, r.OutputLines)
	assert.Equal(t, in, r.Content)
}

func TestTruncateHead_LineLimitHit(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, "x")
	}
	in := strings.Join(lines, "\n")
	r := tool.TruncateHead(in, tool.TruncationOptions{MaxLines: 3, MaxBytes: 10000})
	assert.True(t, r.Truncated)
	assert.Equal(t, tool.TruncatedByLines, r.TruncatedBy)
	assert.Equal(t, 3, r.OutputLines)
	assert.Equal(t, "x\nx\nx", r.Content)
}

func TestTruncateHead_ByteLimitHit(t *testing.T) {
	// 10 lines of 100 chars, budget of 250 bytes -> ~2 lines fit
	// (100 + 1 + 100 = 201; next would be 201+1+100 = 302 > 250).
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, strings.Repeat("a", 100))
	}
	in := strings.Join(lines, "\n")
	r := tool.TruncateHead(in, tool.TruncationOptions{MaxLines: 1000, MaxBytes: 250})
	assert.True(t, r.Truncated)
	assert.Equal(t, tool.TruncatedByBytes, r.TruncatedBy)
	assert.Equal(t, 2, r.OutputLines)
	assert.Equal(t, 201, r.OutputBytes)
	assert.False(t, r.FirstLineExceedsLimit)
}

func TestTruncateHead_FirstLineExceedsLimit(t *testing.T) {
	in := strings.Repeat("x", 500)
	r := tool.TruncateHead(in, tool.TruncationOptions{MaxLines: 2000, MaxBytes: 100})
	assert.True(t, r.Truncated)
	assert.True(t, r.FirstLineExceedsLimit)
	assert.Equal(t, tool.TruncatedByBytes, r.TruncatedBy)
	assert.Equal(t, "", r.Content)
}

func TestTruncateHead_ExactLineLimit(t *testing.T) {
	in := "a\nb\nc"
	r := tool.TruncateHead(in, tool.TruncationOptions{MaxLines: 3, MaxBytes: 10000})
	assert.False(t, r.Truncated)
}

func TestTruncateTail_UnderBothLimits(t *testing.T) {
	in := "line1\nline2\nline3"
	r := tool.TruncateTail(in, tool.TruncationOptions{})
	assert.False(t, r.Truncated)
	assert.Equal(t, in, r.Content)
}

func TestTruncateTail_LineLimitKeepsTail(t *testing.T) {
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, "x")
	}
	in := strings.Join(lines, "\n")
	r := tool.TruncateTail(in, tool.TruncationOptions{MaxLines: 3, MaxBytes: 10000})
	assert.True(t, r.Truncated)
	assert.Equal(t, 3, r.OutputLines)
	assert.Equal(t, "x\nx\nx", r.Content)
}

func TestTruncateTail_SingleGiantLinePartial(t *testing.T) {
	in := strings.Repeat("x", 500)
	r := tool.TruncateTail(in, tool.TruncationOptions{MaxLines: 1000, MaxBytes: 100})
	assert.True(t, r.Truncated)
	assert.True(t, r.LastLinePartial)
	assert.Equal(t, 100, len(r.Content))
}

func TestFormatSize(t *testing.T) {
	assert.Equal(t, "512B", tool.FormatSize(512))
	assert.Equal(t, "1.0KB", tool.FormatSize(1024))
	assert.Equal(t, "1.5MB", tool.FormatSize(1024*1024*3/2))
}
