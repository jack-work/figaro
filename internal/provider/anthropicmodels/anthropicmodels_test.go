package anthropicmodels

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
