package anthropicmodels

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContextWindow(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"claude-opus-5", 1_000_000},
		{"claude-opus-5-20260115", 1_000_000},
		{"claude-opus-5-5", 1_000_000},
		{"claude-opus-5.5", 1_000_000},
		{"claude-opus-4-8", 1_000_000},
		{"claude-opus-4-7", 1_000_000},
		{"claude-opus-4-6", 1_000_000},
		{"claude-sonnet-5", 1_000_000},
		{"claude-sonnet-4-6", 1_000_000},
		{"claude-sonnet-4-5-20250929", 1_000_000},
		{"claude-fable-5", 1_000_000},
		{"claude-opus-4.6", 1_000_000},
		{"anthropic/claude-opus-5", 1_000_000},
		{"Claude-Opus-5", 1_000_000},

		{"claude-opus-4-5-20251101", 200_000},
		{"claude-opus-4-1-20250805", 200_000},
		{"claude-opus-4-20250514", 200_000},
		{"claude-haiku-4-5-20251001", 200_000},
		{"claude-haiku-4.5", 200_000},
		{"claude-3-5-sonnet-20241022", 200_000},

		{"claude-sonnet-4-20250514", 0}, // delisted; we will not guess
		{"claude-opus-6", 0},
		{"gpt-5.6-terra", 0},
		{"", 0},
		{"claude", 0},
		{"claude-opus-42", 0}, // prefix must land on an id boundary
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			assert.Equal(t, tc.want, ContextWindow(tc.model))
		})
	}
}

func TestCompareVersions(t *testing.T) {
	assert.Equal(t, 0, CompareVersions("2.1.280", "2.1.280"))
	assert.Equal(t, 0, CompareVersions("2.1", "2.1.0"))
	assert.Equal(t, -1, CompareVersions("2.1.267", "2.1.280"))
	assert.Equal(t, 1, CompareVersions("2.1.281", "2.1.280"))
	assert.Equal(t, 1, CompareVersions("2.2.0", "2.1.999"))
	assert.Equal(t, 1, CompareVersions("10.0.0", "9.9.9")) // numeric, not lexical
}

func TestVersionFloor(t *testing.T) {
	assert.Equal(t, "2.1.280", VersionFloor("claude-opus-5-5"))
	assert.Equal(t, "2.1.280", VersionFloor("claude-opus-5-5-20260901"))
	assert.Equal(t, "2.1.280", VersionFloor("anthropic/claude-opus-5.5"))
	assert.Equal(t, "2.1.251", VersionFloor("claude-fable-5-1"))
	assert.Equal(t, "", VersionFloor("claude-opus-5"))    // "5-5" is not "5"
	assert.Equal(t, "", VersionFloor("claude-opus-5-50")) // id boundary
	assert.Equal(t, "", VersionFloor("claude-sonnet-4-5"))
}

// When this goes red, paste `npm view @anthropic-ai/claude-code
// dist-tags.latest` into ClaudeCodeVersion.
func TestClaudeCodeVersionClearsFloors(t *testing.T) {
	for model, floor := range versionFloors {
		assert.GreaterOrEqual(t, CompareVersions(ClaudeCodeVersion, floor), 0,
			"%s requires client %s; ClaudeCodeVersion is %s", model, floor, ClaudeCodeVersion)
	}
}
